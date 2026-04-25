package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"charm.land/huh/v2"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/ui"
	"github.com/kurisu-agent/drift/internal/kart"
	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// newCmd is kept 1:1 with the kart.new RPC params so they don't drift apart.
type newCmd struct {
	Name         string `arg:"" help:"Kart name (matches ^[a-z][a-z0-9-]{0,62}$)."`
	Clone        string `name:"clone" help:"Clone an existing repo (mutually exclusive with --starter)."`
	Starter      string `name:"starter" help:"Template repo; history is discarded after clone."`
	Tune         string `name:"tune" help:"Named preset that provides defaults for other flags."`
	Features     string `name:"features" help:"Devcontainer features JSON, merged with the tune's (additive)."`
	Devcontainer string `name:"devcontainer" help:"Override devcontainer: file path, JSON string, or URL."`
	Dotfiles     string `name:"dotfiles" help:"Layer-2 dotfiles repo URL (overrides tune's dotfiles_repo)."`
	Character    string `name:"character" help:"Git/GitHub identity to inject."`
	Autostart    bool   `name:"autostart" help:"Enable auto-start on server reboot."`
	// Mount is repeatable; format mirrors `docker run --mount`:
	//   --mount type=bind,source=~/.claude,target=/home/dev/.claude
	// A leading `~/` in source is rewritten to `${localEnv:HOME}/` server-
	// side so devpod resolves it against the circuit's env.
	// sep="none" turns off kong's default comma-splitting — mount specs
	// carry commas between their k=v pairs and would otherwise shatter
	// into `type=bind`, `source=X`, `target=Y` as separate entries.
	Mount []string `name:"mount" sep:"none" help:"Extra host-bind or volume mount (repeatable). type=bind,source=X,target=Y"`
	// Connect drops the user into the new kart after a successful create.
	// Default-on for interactive callers; --no-connect preserves the old
	// behavior for scripts that chain `drift new` with their own commands.
	// Auto-connect is also suppressed when stdin is not a TTY or when
	// --output json is in effect, since both imply a non-interactive caller.
	Connect bool `name:"connect" default:"true" negatable:"" help:"Connect to the kart after a successful create (disable with --no-connect)."`
}

func runNew(ctx context.Context, io IO, root *CLI, cmd newCmd, deps deps) int {
	if cmd.Clone != "" && cmd.Starter != "" {
		return errfmt.Emit(io.Stderr, errors.New("--clone and --starter are mutually exclusive"))
	}
	if _, err := parseMountFlags(cmd.Mount); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	expandOwnerRepoShorthand(&cmd)

	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	// Confirm only on the first pass: a name-collision retry loops back
	// here with cmd.Name already updated by promptNewKartName, and asking
	// "create?" right after the user just typed the new name is noise.
	confirmedOnce := false
	var result kart.Result
	for {
		params := buildNewParams(cmd)
		// Pre-RPC summary — the wire call blocks until the whole
		// clone+up+dotfiles flow finishes, so dumping the resolved inputs
		// up front is the cheapest way to give the user a preview of what
		// the server is about to do. Real per-phase events need a
		// streaming RPC; tracked separately.
		writeNewPreflight(io.Stderr, root.Output == "json", circuit, cmd)
		if !confirmedOnce && stdinIsTTY(io.Stdin) && root.Output != "json" {
			ok, cErr := confirmNewKart(circuit, cmd)
			if cErr != nil {
				return errfmt.Emit(io.Stderr, cErr)
			}
			if !ok {
				fmt.Fprintln(io.Stderr, "aborted")
				return 1
			}
			confirmedOnce = true
		}
		// Spinner + transport hint on stderr so `drift new ... | jq` still
		// captures a clean JSON payload on stdout. Suppressed under
		// --debug so the spinner redraw doesn't fight the live devpod
		// output streaming back over the SSH transport's stderr.
		msg := fmt.Sprintf("creating kart %q", cmd.Name)
		quiet := root.Output == "json" || root.Debug
		t := ui.NewTheme(io.Stderr, quiet)
		sp := t.NewSpinner(io.Stderr, ui.SpinnerOptions{Message: msg, Transport: "ssh"})
		start := time.Now()
		callErr := deps.call(ctx, circuit, wire.MethodKartNew, params, &result)
		elapsed := time.Since(start).Round(time.Second)
		if callErr == nil {
			sp.Succeed(fmt.Sprintf("created kart %q in %s", result.Name, elapsed))
			break
		}
		sp.Fail()
		// In-place retry when the server reports a name collision and
		// we're on a real TTY — prompt for a new name and resend.
		// Scripted callers keep the existing error contract.
		var re *rpcerr.Error
		if errors.As(callErr, &re) && re.Type == rpcerr.TypeNameCollision && stdinIsTTY(io.Stdin) {
			newName, pErr := promptNewKartName(io, cmd.Name)
			if pErr != nil {
				return errfmt.Emit(io.Stderr, pErr)
			}
			if newName == "" {
				fmt.Fprintln(io.Stderr, "aborted")
				return 1
			}
			cmd.Name = newName
			continue
		}
		return errfmt.Emit(io.Stderr, callErr)
	}

	if root.Output == "json" {
		return emitJSON(io, result)
	}
	fmt.Fprintf(io.Stdout, "created kart %q\n", result.Name)
	if result.Source.Mode != "none" && result.Source.URL != "" {
		fmt.Fprintf(io.Stdout, "  source: %s (%s)\n", result.Source.URL, result.Source.Mode)
	}
	if result.Tune != "" {
		fmt.Fprintf(io.Stdout, "  tune: %s\n", result.Tune)
	}
	if result.Character != "" {
		fmt.Fprintf(io.Stdout, "  character: %s\n", result.Character)
	}
	if result.Autostart {
		fmt.Fprintln(io.Stdout, "  autostart: enabled")
	}
	if result.Warning != "" {
		fmt.Fprintf(io.Stderr, "warning: %s\n", result.Warning)
	}
	if shouldAutoConnect(cmd, root, io) {
		return doConnect(ctx, io, root, deps, circuit, result.Name, false, false, false,
			nil)
	}
	return 0
}

// shouldAutoConnect decides whether `drift new` drops the caller into the
// new kart. Disabled explicitly with --no-connect, and implicitly when the
// caller looks non-interactive (stdin is a pipe/file, or --output json is
// set so stdout is being parsed).
func shouldAutoConnect(cmd newCmd, root *CLI, io IO) bool {
	if !cmd.Connect {
		return false
	}
	if root.Output == "json" {
		return false
	}
	return stdinIsTTY(io.Stdin)
}

func buildNewParams(cmd newCmd) map[string]any {
	params := map[string]any{"name": cmd.Name}
	if cmd.Clone != "" {
		params["clone"] = cmd.Clone
	}
	if cmd.Starter != "" {
		params["starter"] = cmd.Starter
	}
	if cmd.Tune != "" {
		params["tune"] = cmd.Tune
	}
	if cmd.Features != "" {
		params["features"] = cmd.Features
	}
	if cmd.Devcontainer != "" {
		params["devcontainer"] = cmd.Devcontainer
	}
	if cmd.Dotfiles != "" {
		params["dotfiles"] = cmd.Dotfiles
	}
	if cmd.Character != "" {
		params["character"] = cmd.Character
	}
	if cmd.Autostart {
		params["autostart"] = true
	}
	if len(cmd.Mount) > 0 {
		// Pre-validated in runNew; ignore error here.
		if mounts, err := parseMountFlags(cmd.Mount); err == nil && len(mounts) > 0 {
			params["mounts"] = mounts
		}
	}
	return params
}

// parseMountFlags turns each `--mount` arg into a model.Mount. Syntax mirrors
// docker's `--mount`: comma-separated k=v pairs, with `type`, `source`/`src`,
// `target`/`dst`/`destination`, and `external` being the recognized keys.
// Anything else is passed through verbatim as an entry in Mount.Other so we
// don't have to chase every docker-side flag this fork might add next.
func parseMountFlags(specs []string) ([]model.Mount, error) {
	if len(specs) == 0 {
		return nil, nil
	}
	out := make([]model.Mount, 0, len(specs))
	for _, spec := range specs {
		if strings.TrimSpace(spec) == "" {
			continue
		}
		var m model.Mount
		for _, kv := range strings.Split(spec, ",") {
			kv = strings.TrimSpace(kv)
			if kv == "" {
				continue
			}
			key, val, ok := strings.Cut(kv, "=")
			if !ok {
				return nil, fmt.Errorf("--mount %q: expected key=value pairs, got %q", spec, kv)
			}
			key = strings.TrimSpace(key)
			val = strings.TrimSpace(val)
			switch key {
			case "type":
				m.Type = val
			case "source", "src":
				m.Source = val
			case "target", "dst", "destination":
				m.Target = val
			case "external":
				switch val {
				case "true", "1":
					m.External = true
				case "false", "0", "":
					m.External = false
				default:
					return nil, fmt.Errorf("--mount %q: external=%s must be true or false", spec, val)
				}
			default:
				m.Other = append(m.Other, kv)
			}
		}
		if m.Target == "" {
			return nil, fmt.Errorf("--mount %q: target is required", spec)
		}
		out = append(out, m)
	}
	return out, nil
}

// writeNewPreflight dumps the resolved kart.new inputs to stderr. JSON
// callers get a machine-readable copy on a single line; humans get a
// dim-styled block. Skipped when stderr isn't a real terminal and we're
// in JSON mode (no point echoing to a structured pipeline).
func writeNewPreflight(w interface{ Write(p []byte) (int, error) }, jsonMode bool, circuit string, cmd newCmd) {
	if jsonMode {
		// Single-line JSON: easy to grep out of a log stream.
		rec := map[string]any{
			"event":   "kart.new.preflight",
			"circuit": circuit,
			"params":  buildNewParams(cmd),
		}
		if buf, err := json.Marshal(rec); err == nil {
			fmt.Fprintln(w, string(buf))
		}
		return
	}
	p := ui.NewTheme(w, false)
	fmt.Fprintln(w, p.Dim(fmt.Sprintf("→ kart.new on circuit %q", circuit)))
	fmt.Fprintln(w, p.Dim(fmt.Sprintf("  name:         %s", cmd.Name)))
	if cmd.Clone != "" {
		fmt.Fprintln(w, p.Dim(fmt.Sprintf("  clone:        %s", cmd.Clone)))
	}
	if cmd.Starter != "" {
		fmt.Fprintln(w, p.Dim(fmt.Sprintf("  starter:      %s", cmd.Starter)))
	}
	if cmd.Tune != "" {
		fmt.Fprintln(w, p.Dim(fmt.Sprintf("  tune:         %s", cmd.Tune)))
	}
	if cmd.Character != "" {
		fmt.Fprintln(w, p.Dim(fmt.Sprintf("  character:    %s", cmd.Character)))
	}
	if cmd.Devcontainer != "" {
		fmt.Fprintln(w, p.Dim(fmt.Sprintf("  devcontainer: %s", cmd.Devcontainer)))
	}
	if cmd.Features != "" {
		fmt.Fprintln(w, p.Dim(fmt.Sprintf("  features:     %s", cmd.Features)))
	}
	if cmd.Dotfiles != "" {
		fmt.Fprintln(w, p.Dim(fmt.Sprintf("  dotfiles:     %s", cmd.Dotfiles)))
	}
	if cmd.Autostart {
		fmt.Fprintln(w, p.Dim("  autostart:    enabled"))
	}
	for _, m := range cmd.Mount {
		fmt.Fprintln(w, p.Dim(fmt.Sprintf("  mount:        %s", m)))
	}
}

// confirmNewKart pauses for a y/N confirmation right after the
// preflight, so the user can bail before the server-side
// clone+up+dotfiles flow (which can take minutes) actually starts.
// Defaults to "create" — Enter accepts the summary as printed. The
// description body repeats the resolved params (tune, character,
// source, …) inline so the user doesn't have to scroll past the prompt
// to recheck what they typed.
func confirmNewKart(circuit string, cmd newCmd) (bool, error) {
	val := true
	prompt := huh.NewConfirm().
		Title(fmt.Sprintf("create kart %q on circuit %q?", cmd.Name, circuit)).
		Description(buildNewConfirmSummary(cmd)).
		Affirmative("create").
		Negative("cancel").
		Value(&val)
	if err := huh.NewForm(huh.NewGroup(prompt)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, nil
		}
		return false, err
	}
	return val, nil
}

// buildNewConfirmSummary renders the resolved kart.new params as the
// confirm prompt's Description body. Only populated fields appear, so
// the prompt stays compact when the user accepts the tune's defaults.
// The source row collapses --clone / --starter into one line since
// they're mutually exclusive.
func buildNewConfirmSummary(cmd newCmd) string {
	var rows [][2]string
	if cmd.Clone != "" {
		rows = append(rows, [2]string{"source", cmd.Clone + " (clone)"})
	} else if cmd.Starter != "" {
		rows = append(rows, [2]string{"source", cmd.Starter + " (starter)"})
	}
	if cmd.Tune != "" {
		rows = append(rows, [2]string{"tune", cmd.Tune})
	}
	if cmd.Character != "" {
		rows = append(rows, [2]string{"character", cmd.Character})
	}
	if cmd.Devcontainer != "" {
		rows = append(rows, [2]string{"devcontainer", cmd.Devcontainer})
	}
	if cmd.Features != "" {
		rows = append(rows, [2]string{"features", cmd.Features})
	}
	if cmd.Dotfiles != "" {
		rows = append(rows, [2]string{"dotfiles", cmd.Dotfiles})
	}
	if cmd.Autostart {
		rows = append(rows, [2]string{"autostart", "enabled"})
	}
	for _, m := range cmd.Mount {
		rows = append(rows, [2]string{"mount", m})
	}
	if len(rows) == 0 {
		return ""
	}
	var b strings.Builder
	for _, r := range rows {
		fmt.Fprintf(&b, "%-12s  %s\n", r[0]+":", r[1])
	}
	return strings.TrimRight(b.String(), "\n")
}

// promptNewKartName asks for a replacement name after a name_collision
// via huh, matching the other interactive prompts. Blank input cancels
// (returns ""); ctrl-C / esc also cancels via huh.ErrUserAborted.
func promptNewKartName(io IO, taken string) (string, error) {
	p := ui.NewTheme(io.Stderr, false)
	fmt.Fprintf(io.Stderr, "%s kart %q already exists on this circuit.\n", p.Warn("!"), taken)
	var val string
	input := huh.NewInput().
		Title("new name (empty to cancel)").
		Value(&val)
	if err := huh.NewForm(huh.NewGroup(input)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", nil
		}
		return "", err
	}
	return strings.TrimSpace(val), nil
}

// expandOwnerRepoShorthand lets `drift new owner/repo` stand in for
// `drift new repo --clone https://github.com/owner/repo`. Only fires when
// the name is a single `owner/repo` slug and no explicit --clone or
// --starter was passed — anything else is forwarded untouched so the
// server's existing name validator gives the real error.
func expandOwnerRepoShorthand(cmd *newCmd) {
	if cmd.Clone != "" || cmd.Starter != "" {
		return
	}
	parts := strings.Split(cmd.Name, "/")
	if len(parts) != 2 {
		return
	}
	owner, repo := parts[0], parts[1]
	if owner == "" || repo == "" {
		return
	}
	cmd.Name = repo
	cmd.Clone = "https://github.com/" + owner + "/" + repo
}
