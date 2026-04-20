package drift

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/progress"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/kart"
	"github.com/kurisu-agent/drift/internal/rpc/client"
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
}

func runNew(ctx context.Context, io IO, root *CLI, cmd newCmd, deps deps) int {
	if cmd.Clone != "" && cmd.Starter != "" {
		return errfmt.Emit(io.Stderr, errors.New("--clone and --starter are mutually exclusive"))
	}
	expandOwnerRepoShorthand(&cmd)

	circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	rpcc := client.New()
	var result kart.Result
	for {
		params := buildNewParams(cmd)
		// Pre-RPC summary — the wire call blocks until the whole
		// clone+up+dotfiles flow finishes, so dumping the resolved inputs
		// up front is the cheapest way to give the user a preview of what
		// the server is about to do. Real per-phase events need a
		// streaming RPC; tracked separately.
		writeNewPreflight(io.Stderr, root.Output == "json", circuit, cmd)
		// Spinner + transport hint on stderr so `drift new ... | jq` still
		// captures a clean JSON payload on stdout.
		msg := fmt.Sprintf("creating kart %q", cmd.Name)
		ph := progress.Start(io.Stderr, root.Output == "json", msg, "ssh")
		start := time.Now()
		callErr := rpcc.Call(ctx, circuit, wire.MethodKartNew, params, &result)
		elapsed := time.Since(start).Round(time.Second)
		if callErr == nil {
			ph.Succeed(fmt.Sprintf("created kart %q in %s", result.Name, elapsed))
			break
		}
		ph.Fail()
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
		buf, mErr := json.Marshal(result)
		if mErr != nil {
			return errfmt.Emit(io.Stderr, mErr)
		}
		fmt.Fprintln(io.Stdout, string(buf))
		return 0
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
	return 0
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
	return params
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
	p := style.For(w, false)
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
}

// promptNewKartName asks for a replacement name after a name_collision.
// Blank input cancels (returns ""). EOF is treated as cancel so ctrl-D
// matches the behavior of every other prompt in the codebase.
func promptNewKartName(io IO, taken string) (string, error) {
	p := style.For(io.Stderr, false)
	fmt.Fprintf(io.Stderr, "%s kart %q already exists on this circuit.\n", p.Warn("!"), taken)
	fmt.Fprint(io.Stderr, "  new name (empty to cancel): ")
	br := bufio.NewReader(io.Stdin)
	line, err := br.ReadString('\n')
	if err != nil && line == "" {
		return "", nil
	}
	return strings.TrimSpace(line), nil
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
