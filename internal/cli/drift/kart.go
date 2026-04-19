package drift

import (
	"encoding/json"
	"errors"
	"fmt"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/config"
)

// resolveCircuit: -c wins, falling back to default_circuit. Empty is an
// error — every kart verb requires a target.
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
