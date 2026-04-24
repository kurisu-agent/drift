package server

import (
	"context"
	"encoding/json"

	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// kartConnectHandler assembles the remote-command argv that `drift connect`
// hands to its ssh/mosh transport. Centralizing this here (instead of
// having the client hand-build `devpod ssh <kart>`) gives lakitu a single
// authoritative place to:
//
//   - Name the actual devpod binary with an absolute path — on NixOS
//     and other declarative systems, /usr/bin/devpod may not exist and
//     lakitu's own pinned copy under ~/.drift/bin/devpod is what's trusted.
//   - Inject `DEVPOD_HOME` via a `env KEY=VALUE` prefix on the argv, so
//     the setting lives on the remote command line for that ONE process
//     and never leaks into the user's shell. (The old workaround put
//     DEVPOD_HOME in /etc/pam/environment, which rerouted every `devpod`
//     invocation for every user-facing shell session — way too broad.)
//   - Fold in the kart-scoped session-env resolution so `drift connect`
//     only pays one RPC roundtrip. The separate kart.session_env method
//     stays registered for older-client compatibility.
//
// An older lakitu without this method returns `method_not_found`; the
// client then falls back to the pre-kart.connect hand-built shape.
func (d KartDeps) kartConnectHandler(ctx context.Context, params json.RawMessage) (any, error) {
	p, err := bindLifecycleParams(params, "kart.connect")
	if err != nil {
		return nil, err
	}
	if d.Devpod == nil {
		return nil, rpcerr.Internal("kart.connect: devpod client not configured")
	}
	// Confirm the kart exists in the garage before returning a connect
	// command — otherwise the client would ssh in and waste time on
	// devpod's own (less friendly) not-found message. Missing-from-
	// garage is the same condition kartInfo / kartStart use for
	// kart_not_found; reuse it.
	_, ok, err := d.readKartConfig(p.Name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, rpcerr.NotFound(rpcerr.TypeKartNotFound,
			"kart %q not found", p.Name).With("kart", p.Name)
	}

	argv := buildKartConnectArgv(d.Devpod, p.Name)

	// Reuse the same chest-backed session env resolution kart.session_env
	// does so a single kart.connect gives the client everything it needs.
	envResult, err := d.kartSessionEnvHandler(ctx, params)
	if err != nil {
		return nil, err
	}
	if ser, ok := envResult.(KartSessionEnvResult); ok {
		for _, kv := range ser.Env {
			argv = append(argv, "--set-env", kv)
		}
	}

	return wire.KartConnectResult{Argv: argv}, nil
}

// buildKartConnectArgv produces the bare `env DEVPOD_HOME=... devpod ssh
// <kart> --agent-forwarding=false` prefix. Split out so the test can pin
// the exact shape without spinning up a full KartDeps fixture. DevpodHome
// empty means "don't inject" — covers the fallback for tests / zero-value
// clients; in production lakitu always sets it.
//
// `--agent-forwarding=false` is pinned because drift's model puts
// credentials on the server (characters + chest), so forwarding the
// workstation's ssh-agent into the container is both unnecessary and
// actively harmful: devpod inherits SSH_AUTH_SOCK from the outer
// transport, and under mosh that socket is always stale by the time
// devpod dials it (mosh-server detaches, outer ssh closes, sshd unlinks
// the socket), causing `devpod ssh` to exit fatal before the session
// starts.
func buildKartConnectArgv(c *devpod.Client, kart string) []string {
	binary := devpod.DefaultBinary
	if c != nil && c.Binary != "" {
		binary = c.Binary
	}
	var argv []string
	if c != nil && c.DevpodHome != "" {
		argv = append(argv, "env", "DEVPOD_HOME="+c.DevpodHome)
	}
	argv = append(argv, binary, "ssh", kart, "--agent-forwarding=false")
	return argv
}

// RegisterKartConnect registers the kart.connect RPC. Separate from
// RegisterKartLifecycle so tests can compose a minimal handler surface
// and readers can trace this one codepath end-to-end without wading
// through start/stop/restart/delete.
func RegisterKartConnect(reg *rpc.Registry, d KartDeps) {
	reg.Register(wire.MethodKartConnect, d.kartConnectHandler)
}
