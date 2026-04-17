package drift

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kurisu-agent/drift/internal/config"
)

// resolveCircuit returns the circuit name to target for a remote RPC. The
// `-c` root flag wins; otherwise the default circuit from the client config
// is used. An empty result is an error — every kart verb requires a target.
func resolveCircuit(root *CLI, deps deps) (string, error) {
	if root != nil && root.Circuit != "" {
		return root.Circuit, nil
	}
	cfgPath, err := deps.clientConfigPath()
	if err != nil {
		return "", err
	}
	cfg, err := config.LoadClient(cfgPath)
	if err != nil {
		return "", err
	}
	if cfg.DefaultCircuit == "" {
		return "", errors.New("no circuit specified and no default_circuit in client config (drift circuit add --default)")
	}
	return cfg.DefaultCircuit, nil
}

// emitKartResult is the shared formatter for start/stop/restart/delete. The
// text rendering is deliberately terse so stdout stays scriptable; JSON
// output echoes the server's result verbatim.
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
		return emitError(io, err)
	}
	fmt.Fprintf(io.Stdout, "%s kart %q (status %s)\n", verb, res.Name, res.Status)
	return 0
}
