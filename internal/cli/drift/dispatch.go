package drift

import (
	"context"
	"fmt"
	osexec "os/exec"
	"strings"

	"github.com/kurisu-agent/drift/internal/cli/style"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/wire"
)

// Shared remote-command dispatch helpers used by `drift ai`, `drift skill`,
// and (historically) `drift run`. Kept in one file because they share
// transport decisions and post-exit hook plumbing.

// moshOnPath reports whether the local mosh binary is resolvable. A
// client without mosh falls back to ssh regardless of what the circuit
// supports.
func moshOnPath() bool {
	_, err := osexec.LookPath("mosh")
	return err == nil
}

// buildRunArgv shapes the ssh/mosh command line for one remote
// invocation. Interactive mode asks for a PTY (-t for ssh; mosh always
// gets one). Output mode disables PTY allocation (-T) so the remote
// command writes through uncluttered — important for pipelines.
func buildRunArgv(mode wire.RunMode, useMosh bool, circuit string, forwardAgent bool, remoteCmd string) (string, []string) {
	target := "drift." + circuit
	if useMosh {
		return "mosh", []string{target, "--", "sh", "-c", remoteCmd}
	}
	var args []string
	if mode == wire.RunModeInteractive {
		args = append(args, "-t")
	} else {
		args = append(args, "-T")
	}
	if forwardAgent {
		args = append(args, "-A")
	}
	args = append(args, target, remoteCmd)
	return "ssh", args
}

// runPostHook dispatches the named client-side hook. An unknown hook
// name is treated as a hard error — the server-side registry should
// never ship a hook the client doesn't handle, but if it does we'd
// rather surface the mismatch than silently swallow the handoff.
func runPostHook(ctx context.Context, io IO, root *CLI, deps deps, circuit string, hook wire.RunPostHook) error {
	switch hook {
	case wire.RunPostNone:
		return nil
	case wire.RunPostConnectLastScaffold:
		return connectLastScaffold(ctx, io, root, deps, circuit)
	default:
		return fmt.Errorf("server returned unknown post hook %q — upgrade drift", hook)
	}
}

// connectLastScaffold reads ~/.drift/last-scaffold over SSH and, if a
// kart name is present, invokes runConnect on it. Missing / empty file
// is not an error — the skill session may have decided not to produce
// a kart (user aborted, etc.) and we exit cleanly.
func connectLastScaffold(ctx context.Context, io IO, root *CLI, deps deps, circuit string) error {
	name, err := readLastScaffold(ctx, circuit)
	if err != nil {
		return fmt.Errorf("post-hook: read handoff sentinel: %w", err)
	}
	if name == "" {
		p := style.For(io.Stderr, root.Output == "json")
		if p.Enabled {
			fmt.Fprintln(io.Stderr, p.Dim("session exited without writing ~/.drift/last-scaffold — skipping connect"))
		}
		return nil
	}
	p := style.For(io.Stderr, root.Output == "json")
	if p.Enabled {
		fmt.Fprintln(io.Stderr, p.Dim("→ connecting to scaffolded kart "+name))
	}
	rc := runConnect(ctx, io, root, connectCmd{Name: name}, deps)
	if rc != 0 {
		return fmt.Errorf("post-hook: auto-connect to %q failed (exit %d)", name, rc)
	}
	return nil
}

// readLastScaffold is a small one-shot ssh that prints the sentinel
// file contents (or "" if missing). Runs as an output-mode child — no
// PTY, no stdin — so its stdout is clean enough to parse.
func readLastScaffold(ctx context.Context, circuit string) (string, error) {
	target := "drift." + circuit
	// test -f … gate avoids ssh-side "cat: ...: No such file" noise when
	// the file is simply absent, which we want to treat as empty.
	remote := `if [ -s "$HOME/.drift/last-scaffold" ]; then cat "$HOME/.drift/last-scaffold"; fi`
	cmd := driftexec.Cmd{
		Name: "ssh",
		Args: []string{"-T", target, remote},
	}
	res, err := driftexec.Run(ctx, cmd)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(res.Stdout)), nil
}
