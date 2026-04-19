package drift

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/sshconf"
	"github.com/kurisu-agent/drift/internal/wire"
)

type circuitCmd struct {
	Add  circuitAddCmd  `cmd:"" help:"Register a circuit (probes for name, updates client config + SSH config)."`
	Rm   circuitRmCmd   `cmd:"" help:"Unregister a circuit."`
	List circuitListCmd `cmd:"" help:"List configured circuits."`
	Set  circuitSetCmd  `cmd:"" help:"Set a config field on the target circuit (e.g. name)."`
}

// circuitAddCmd: the positional arg is the raw SSH destination. The
// canonical circuit name is discovered via server.info — clients don't pick
// names, circuits advertise them.
type circuitAddCmd struct {
	// UserHost is optional when --tailscale is passed (the picker fills it).
	UserHost    string `arg:"" name:"user@host" optional:"" help:"SSH destination, e.g. alice@devbox or alice@devbox:2222."`
	Tailscale   bool   `name:"tailscale" help:"Pick the destination from online tailscale peers (mutually exclusive with user@host)."`
	Default     bool   `name:"default" help:"Set as the default circuit."`
	NoSSHConfig bool   `name:"no-ssh-config" help:"Skip writing ~/.ssh/config and ~/.config/drift/ssh_config."`
}

type circuitRmCmd struct {
	Name string `arg:"" help:"Circuit name to remove."`
}

type circuitListCmd struct{}

// circuitSetCmd namespaces `drift circuit set <key> <value>` so we can
// extend to more mutable fields later without growing new subcommands.
type circuitSetCmd struct {
	Name    circuitSetNameCmd    `cmd:"" help:"Rename the target circuit (rewrites server config + local alias)."`
	Default circuitSetDefaultCmd `cmd:"" help:"Choose which configured circuit is the default (interactive picker when no name given)."`
}

type circuitSetNameCmd struct {
	NewName string `arg:"" name:"new-name" help:"New circuit name."`
}

type circuitSetDefaultCmd struct {
	Name string `arg:"" optional:"" help:"Circuit name to set as default. Omit for an interactive picker."`
}

// runCircuitAdd probes the raw SSH target for the canonical circuit name,
// then writes the client config + SSH block keyed off that name. An
// already-present name pointing at a different host is a collision error —
// rename on the server first.
func runCircuitAdd(ctx context.Context, io IO, root *CLI, cmd circuitAddCmd, deps deps) int {
	if cmd.Tailscale && cmd.UserHost != "" {
		return errfmt.Emit(io.Stderr, rpcerr.UserError(rpcerr.TypeMutuallyExclusive,
			"circuit add: --tailscale and user@host are mutually exclusive"))
	}
	if cmd.Tailscale {
		target, ok, err := tailscalePicker(ctx, io.Stdin, io.Stderr)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if !ok {
			fmt.Fprintln(io.Stderr, "aborted")
			return 1
		}
		cmd.UserHost = target
	}
	if cmd.UserHost == "" {
		return errfmt.Emit(io.Stderr, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"circuit add: user@host is required (or pass --tailscale to pick from peers)"))
	}
	userPart, hostPart, err := name.SplitUserHost(cmd.UserHost)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if userPart == "" {
		return errfmt.Emit(io.Stderr, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"circuit add: user is required (use alice@host, not bare host)"))
	}

	sshArgs, err := name.SSHArgsFor(cmd.UserHost)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if deps.probeInfo == nil {
		return errfmt.Emit(io.Stderr, rpcerr.Internal("circuit add: probeInfo not configured"))
	}
	info, err := deps.probeInfo(ctx, sshArgs)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if !config.CircuitNameRE.MatchString(info.Name) {
		return errfmt.Emit(io.Stderr, rpcerr.Internal(
			"server returned invalid circuit name %q (operator: set `drift circuit set name <slug>` on the circuit)", info.Name))
	}

	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if cfg.Circuits == nil {
		cfg.Circuits = make(map[string]config.ClientCircuit)
	}
	if existing, ok := cfg.Circuits[info.Name]; ok && existing.Host != cmd.UserHost {
		return errfmt.Emit(io.Stderr, rpcerr.Conflict(rpcerr.TypeNameCollision,
			"circuit %q already configured for %s (rename the new server via `drift circuit set name <other-name>` or remove the existing entry with `drift circuit rm %s`)",
			info.Name, existing.Host, info.Name).With("circuit", info.Name).With("existing_host", existing.Host))
	}
	cfg.Circuits[info.Name] = config.ClientCircuit{Host: cmd.UserHost}
	if cmd.Default || cfg.DefaultCircuit == "" {
		cfg.DefaultCircuit = info.Name
	}
	if err := config.SaveClient(cfgPath, cfg); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	manageSSH := cfg.ManagesSSHConfig() && !cmd.NoSSHConfig
	if manageSSH {
		mgr, err := sshManagerFor(cfgPath)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if err := mgr.EnsureInclude(userSSHConfigPath()); err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if err := mgr.EnsureSocketsDir(); err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if err := mgr.WriteCircuitBlock(info.Name, hostPart, userPart); err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if err := mgr.EnsureWildcardBlock(); err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
	}

	return emitCircuitAdd(io, root, info, cfg, manageSSH)
}

// runCircuitRm leaves the ~/.ssh/config Include intact — other circuits
// may still need it.
func runCircuitRm(io IO, root *CLI, cmd circuitRmCmd, deps deps) int {
	if err := name.Validate("circuit", cmd.Name); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if _, ok := cfg.Circuits[cmd.Name]; !ok {
		return errfmt.Emit(io.Stderr, fmt.Errorf("circuit %q not found", cmd.Name))
	}
	delete(cfg.Circuits, cmd.Name)
	if cfg.DefaultCircuit == cmd.Name {
		cfg.DefaultCircuit = ""
	}
	if err := config.SaveClient(cfgPath, cfg); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	if cfg.ManagesSSHConfig() {
		mgr, err := sshManagerFor(cfgPath)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if err := mgr.RemoveCircuitBlock(cmd.Name); err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
	}

	return emitCircuitRm(io, root, cmd.Name)
}

func runCircuitList(io IO, root *CLI, deps deps) int {
	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	names := make([]string, 0, len(cfg.Circuits))
	for n := range cfg.Circuits {
		names = append(names, n)
	}
	sort.Strings(names)

	type entry struct {
		Name    string `json:"name"`
		Host    string `json:"host"`
		Default bool   `json:"default"`
	}
	entries := make([]entry, 0, len(names))
	for _, n := range names {
		entries = append(entries, entry{
			Name:    n,
			Host:    cfg.Circuits[n].Host,
			Default: n == cfg.DefaultCircuit,
		})
	}

	if root.Output == "json" {
		payload := struct {
			Circuits []entry `json:"circuits"`
			Default  string  `json:"default_circuit"`
		}{Circuits: entries, Default: cfg.DefaultCircuit}
		buf, err := json.Marshal(payload)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		fmt.Fprintln(io.Stdout, string(buf))
		return 0
	}

	if len(entries) == 0 {
		fmt.Fprintln(io.Stdout, "no circuits configured")
		return 0
	}
	p := style.For(io.Stdout, root.Output == "json")
	rows := make([][]string, 0, len(entries))
	for _, e := range entries {
		def := ""
		if e.Default {
			def = "*"
		}
		rows = append(rows, []string{e.Name, e.Host, def})
	}
	writeTable(io.Stdout, p, []string{"NAME", "HOST", "DEFAULT"}, rows,
		func(_, col int, _ *style.Palette) lipgloss.Style {
			if col == 0 {
				return lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
			}
			return lipgloss.NewStyle()
		})
	return 0
}

func emitCircuitAdd(io IO, root *CLI, info *wire.ServerInfo, cfg *config.Client, manageSSH bool) int {
	if root.Output == "json" {
		payload := struct {
			Circuit    string `json:"circuit"`
			Host       string `json:"host"`
			Default    bool   `json:"default"`
			ManagedSSH bool   `json:"managed_ssh_config"`
			Lakitu     string `json:"lakitu_version"`
			API        int    `json:"api"`
		}{
			Circuit:    info.Name,
			Host:       cfg.Circuits[info.Name].Host,
			Default:    cfg.DefaultCircuit == info.Name,
			ManagedSSH: manageSSH,
			Lakitu:     info.Version,
			API:        info.API,
		}
		buf, err := json.Marshal(payload)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		fmt.Fprintln(io.Stdout, string(buf))
		return 0
	}

	p := style.For(io.Stdout, false)
	fmt.Fprintf(io.Stdout, "registered circuit %s (host %s)\n",
		p.Accent(info.Name), cfg.Circuits[info.Name].Host)
	if cfg.DefaultCircuit == info.Name {
		fmt.Fprintln(io.Stdout, p.Dim("  set as default circuit"))
	}
	if manageSSH {
		fmt.Fprintln(io.Stdout, p.Dim("  wrote SSH config block drift."+info.Name))
	}
	fmt.Fprintf(io.Stdout, "  %s lakitu %s (api %d)\n", p.Dim("probe ok —"), info.Version, info.API)
	return 0
}

func emitCircuitRm(io IO, root *CLI, circuitName string) int {
	if root.Output == "json" {
		payload := struct {
			Circuit string `json:"circuit"`
			Removed bool   `json:"removed"`
		}{Circuit: circuitName, Removed: true}
		buf, err := json.Marshal(payload)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		fmt.Fprintln(io.Stdout, string(buf))
		return 0
	}
	fmt.Fprintf(io.Stdout, "removed circuit %q\n", circuitName)
	return 0
}

// runCircuitSetName renames the circuit end-to-end: it updates the server
// via config.set, then rewrites the client-side config entry + SSH block
// under the new name so the local alias tracks the server's truth.
func runCircuitSetName(ctx context.Context, io IO, root *CLI, cmd circuitSetNameCmd, deps deps) int {
	if err := name.Validate("circuit", cmd.NewName); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	oldName, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if oldName == cmd.NewName {
		fmt.Fprintf(io.Stdout, "circuit %q is already named %q\n", oldName, cmd.NewName)
		return 0
	}

	// Push the rename to the server first — if this fails, the client-side
	// config still matches reality on disk.
	if err := deps.call(ctx, oldName, wire.MethodConfigSet,
		map[string]string{"key": "name", "value": cmd.NewName}, nil); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	// Update local config + SSH block to use the new name.
	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	entry, ok := cfg.Circuits[oldName]
	if !ok {
		return errfmt.Emit(io.Stderr, fmt.Errorf("circuit %q not found in local config", oldName))
	}
	if _, collides := cfg.Circuits[cmd.NewName]; collides {
		return errfmt.Emit(io.Stderr, rpcerr.Conflict(rpcerr.TypeNameCollision,
			"local circuit %q already exists; server rename succeeded but local rewrite blocked — remove the old entry with `drift circuit rm %s`",
			cmd.NewName, cmd.NewName).With("circuit", cmd.NewName))
	}
	delete(cfg.Circuits, oldName)
	cfg.Circuits[cmd.NewName] = entry
	if cfg.DefaultCircuit == oldName {
		cfg.DefaultCircuit = cmd.NewName
	}
	if err := config.SaveClient(cfgPath, cfg); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	if cfg.ManagesSSHConfig() {
		mgr, err := sshManagerFor(cfgPath)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		userPart, hostPart, err := name.SplitUserHost(entry.Host)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if err := mgr.RemoveCircuitBlock(oldName); err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if err := mgr.WriteCircuitBlock(cmd.NewName, hostPart, userPart); err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if err := mgr.EnsureWildcardBlock(); err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
	}

	if root.Output == "json" {
		buf, err := json.Marshal(struct {
			Old string `json:"old_name"`
			New string `json:"new_name"`
		}{Old: oldName, New: cmd.NewName})
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		fmt.Fprintln(io.Stdout, string(buf))
		return 0
	}
	fmt.Fprintf(io.Stdout, "renamed circuit %q → %q\n", oldName, cmd.NewName)
	return 0
}

// runCircuitSetDefault flips which circuit in the local config is
// treated as default. Name-arg form is scriptable; no-arg form launches a
// picker (TTY-only) so users don't have to type the name.
func runCircuitSetDefault(io IO, root *CLI, cmd circuitSetDefaultCmd, deps deps) int {
	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if len(cfg.Circuits) == 0 {
		return errfmt.Emit(io.Stderr, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"no circuits configured — try `drift circuit add user@host` first"))
	}

	target := cmd.Name
	if target == "" {
		picked, ok, err := pickCircuitDefault(io, cfg)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if !ok {
			fmt.Fprintln(io.Stderr, "aborted")
			return 1
		}
		target = picked
	}
	if _, ok := cfg.Circuits[target]; !ok {
		return errfmt.Emit(io.Stderr, rpcerr.NotFound(rpcerr.TypeKartNotFound,
			"circuit %q not found", target).With("circuit", target))
	}
	if cfg.DefaultCircuit == target {
		fmt.Fprintf(io.Stdout, "%q is already the default circuit\n", target)
		return 0
	}
	cfg.DefaultCircuit = target
	if err := config.SaveClient(cfgPath, cfg); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	if root.Output == "json" {
		buf, err := json.Marshal(struct {
			DefaultCircuit string `json:"default_circuit"`
		}{DefaultCircuit: target})
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		fmt.Fprintln(io.Stdout, string(buf))
		return 0
	}
	p := style.For(io.Stdout, false)
	fmt.Fprintf(io.Stdout, "default circuit → %s\n", p.Accent(target))
	return 0
}

// pickCircuitDefault renders a numbered list on stderr, reads a choice on
// stdin, and returns the picked name. Non-TTY stdin is a user error —
// scripted callers should pass the name as an argument.
func pickCircuitDefault(io IO, cfg *config.Client) (string, bool, error) {
	if !stdinIsTTY(io.Stdin) {
		return "", false, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"circuit set default: name arg required on non-interactive stdin")
	}
	names := make([]string, 0, len(cfg.Circuits))
	for n := range cfg.Circuits {
		names = append(names, n)
	}
	sort.Strings(names)

	p := style.For(io.Stderr, false)
	fmt.Fprintln(io.Stderr, p.Bold("circuits:"))
	rows := make([][]string, 0, len(names))
	defaultRow := -1
	for i, n := range names {
		marker := ""
		if n == cfg.DefaultCircuit {
			marker = p.Dim("(current default)")
			defaultRow = i
		}
		rows = append(rows, []string{
			fmt.Sprintf("[%d]", i+1),
			n,
			cfg.Circuits[n].Host,
			marker,
		})
	}
	_ = defaultRow
	writeTable(io.Stderr, p, []string{"", "NAME", "HOST", ""}, rows,
		func(_, col int, _ *style.Palette) lipgloss.Style {
			if col == 1 {
				return lipgloss.NewStyle().Foreground(lipgloss.Color("6"))
			}
			return lipgloss.NewStyle()
		})

	br := bufio.NewReader(io.Stdin)
	fmt.Fprint(io.Stderr, "pick (number, empty to cancel): ")
	line, err := br.ReadString('\n')
	if err != nil && !strings.HasSuffix(err.Error(), "EOF") {
		return "", false, err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", false, nil
	}
	idx, err := strconv.Atoi(line)
	if err != nil || idx < 1 || idx > len(names) {
		return "", false, fmt.Errorf("invalid pick %q (expected 1..%d)", line, len(names))
	}
	return names[idx-1], true, nil
}

func sshManagerFor(cfgPath string) (*sshconf.Manager, error) {
	cfgDir := filepath.Dir(cfgPath)
	managed := filepath.Join(cfgDir, "ssh_config")
	sockets := filepath.Join(cfgDir, "sockets")
	return sshconf.New(sshconf.Paths{
		UserSSHConfig:    userSSHConfigPath(),
		ManagedSSHConfig: managed,
		SocketsDir:       sockets,
	}, sshconf.Options{Manage: true}), nil
}

func userSSHConfigPath() string {
	home, err := userHome()
	if err != nil {
		return ""
	}
	return filepath.Join(home, ".ssh", "config")
}
