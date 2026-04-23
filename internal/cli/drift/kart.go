package drift

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/config"
)

// resolveCircuit: -c wins, falling back to default_circuit. Empty is an
// error — every kart verb requires a target. Returns the loaded client
// config alongside the resolved name so callers that need both can avoid
// a second LoadClient round-trip.
func resolveCircuit(root *CLI, deps deps) (*config.Client, string, error) {
	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return nil, "", err
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return nil, "", err
	}
	if root != nil && root.Circuit != "" {
		return cfg, root.Circuit, nil
	}
	if cfg.DefaultCircuit == "" {
		return nil, "", errors.New("no circuit specified and no default_circuit in client config (drift circuit add --default)")
	}
	return cfg, cfg.DefaultCircuit, nil
}

// kartCmd is the `drift kart …` namespace: connect + all the lifecycle
// verbs. `Connect` is the default subcommand so bare `drift kart` drops
// into the cross-circuit picker. The print-only `drift karts` (plural)
// lives at the top level — see kartsCmd in list.go.
type kartCmd struct {
	Connect kartConnectCmd `cmd:"" default:"withargs" help:"Connect to a kart via mosh (ssh fallback)."`
	Info    infoCmd        `cmd:"" help:"Show a single kart's info."`
	Start   startCmd       `cmd:"" help:"Start a kart (idempotent)."`
	Stop    stopCmd        `cmd:"" help:"Stop a kart (idempotent)."`
	Restart restartCmd     `cmd:"" help:"Restart a kart."`
	Delete  deleteCmd      `cmd:"" help:"Delete a kart (errors if missing)."`
	Logs    logsCmd        `cmd:"" help:"Fetch a chunk of kart logs."`
	Enable  enableCmd      `cmd:"" help:"Enable kart autostart on circuit reboot (idempotent)."`
	Disable disableCmd     `cmd:"" help:"Disable kart autostart (idempotent)."`
}

// kartConnectCmd has the same flag surface as `drift connect` so the two
// stay interchangeable for users with the kart name in hand. The merged
// picker on bare `drift connect` is what makes this command worth its
// own verb: this one always lands on a kart.
type kartConnectCmd struct {
	Name         string   `arg:"" optional:"" help:"Kart name; omit on a TTY to pick from a cross-circuit kart list."`
	SSHArgs      []string `arg:"" optional:"" passthrough:"" help:"Extra flags forwarded to ssh (e.g. -- -i ~/.ssh/id_lab). Under mosh, wrapped into --ssh=\"ssh …\" for the bootstrap."`
	SSH          bool     `name:"ssh" help:"Force plain SSH (skip mosh)."`
	ForwardAgent bool     `name:"forward-agent" help:"Enable SSH agent forwarding (-A)."`
}

// emitKartResult: terse text so stdout stays scriptable; JSON passes
// through verbatim.
func emitKartResult(io IO, root *CLI, verb string, raw json.RawMessage) int {
	if root != nil && root.Output == "json" {
		fmt.Fprintln(io.Stdout, string(raw))
		return 0
	}
	var res struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	fmt.Fprintf(io.Stdout, "%s kart %q (status %s)\n", verb, res.Name, res.Status)
	return 0
}
