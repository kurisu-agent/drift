package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/sshconf"
)

type circuitCmd struct {
	Add  circuitAddCmd  `cmd:"" help:"Register a circuit (updates client config + SSH config)."`
	Rm   circuitRmCmd   `cmd:"" help:"Unregister a circuit."`
	List circuitListCmd `cmd:"" help:"List configured circuits."`
}

type circuitAddCmd struct {
	Name        string `arg:"" help:"Circuit name (lowercase, matches ^[a-z][a-z0-9-]{0,62}$)."`
	Host        string `name:"host" help:"SSH destination, e.g. user@host or user@host:port." required:""`
	Default     bool   `name:"default" help:"Set as the default circuit."`
	NoSSHConfig bool   `name:"no-ssh-config" help:"Skip writing ~/.ssh/config and ~/.config/drift/ssh_config."`
	NoProbe     bool   `name:"no-probe" help:"Skip the server.version probe after writing config."`
}

type circuitRmCmd struct {
	Name string `arg:"" help:"Circuit name to remove."`
}

type circuitListCmd struct{}

// runCircuitAdd: probe failures are reported but don't abort — user retries.
func runCircuitAdd(ctx context.Context, io IO, root *CLI, cmd circuitAddCmd, deps deps) int {
	if err := name.Validate("circuit", cmd.Name); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if strings.TrimSpace(cmd.Host) == "" {
		return errfmt.Emit(io.Stderr, errors.New("--host is required"))
	}
	userPart, hostPart, err := name.SplitUserHost(cmd.Host)
	if err != nil {
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
	if cfg.Circuits == nil {
		cfg.Circuits = make(map[string]config.ClientCircuit)
	}
	cfg.Circuits[cmd.Name] = config.ClientCircuit{Host: cmd.Host}
	if cmd.Default || cfg.DefaultCircuit == "" {
		cfg.DefaultCircuit = cmd.Name
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
		if err := mgr.WriteCircuitBlock(cmd.Name, hostPart, userPart); err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if err := mgr.EnsureWildcardBlock(); err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
	}

	var probe *probeResult
	var probeErr error
	if !cmd.NoProbe && deps.probe != nil {
		probe, probeErr = deps.probe(ctx, cmd.Name)
	}

	return emitCircuitAdd(io, root, cmd.Name, cfg, manageSSH, probe, probeErr)
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

func emitCircuitAdd(io IO, root *CLI, circuitName string, cfg *config.Client, manageSSH bool, probe *probeResult, probeErr error) int {
	if root.Output == "json" {
		payload := struct {
			Circuit    string       `json:"circuit"`
			Host       string       `json:"host"`
			Default    bool         `json:"default"`
			ManagedSSH bool         `json:"managed_ssh_config"`
			Probe      *probeResult `json:"probe,omitempty"`
			ProbeError string       `json:"probe_error,omitempty"`
		}{
			Circuit:    circuitName,
			Host:       cfg.Circuits[circuitName].Host,
			Default:    cfg.DefaultCircuit == circuitName,
			ManagedSSH: manageSSH,
			Probe:      probe,
		}
		if probeErr != nil {
			payload.ProbeError = probeErr.Error()
		}
		buf, err := json.Marshal(payload)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		fmt.Fprintln(io.Stdout, string(buf))
		return 0
	}

	fmt.Fprintf(io.Stdout, "registered circuit %q (host %s)\n", circuitName, cfg.Circuits[circuitName].Host)
	if cfg.DefaultCircuit == circuitName {
		fmt.Fprintln(io.Stdout, "  set as default circuit")
	}
	if manageSSH {
		fmt.Fprintln(io.Stdout, "  wrote SSH config block drift."+circuitName)
	}
	switch {
	case probe != nil:
		fmt.Fprintf(io.Stdout, "  probe ok — lakitu %s (api %d, %dms)\n", probe.Version, probe.API, probe.LatencyMS)
	case probeErr != nil:
		fmt.Fprintf(io.Stderr, "warning: probe failed: %v\n", probeErr)
	}
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
