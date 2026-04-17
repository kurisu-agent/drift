package config

import (
	"fmt"
	"regexp"
)

// Client is the workstation-side config, mirroring plans/PLAN.md
// § Client config layout.
type Client struct {
	DefaultCircuit  string                   `yaml:"default_circuit"`
	ManageSSHConfig *bool                    `yaml:"manage_ssh_config,omitempty"`
	Circuits        map[string]ClientCircuit `yaml:"circuits,omitempty"`
}

// ClientCircuit is a single entry under `circuits:` in the client config.
type ClientCircuit struct {
	Host string `yaml:"host"`
}

// circuitNameRE matches lowercase alphanumeric + hyphen, 1–63 chars, starting
// with a letter. Mirrors the kart-name regex from plans/PLAN.md § drift new
// flags — circuit names share the same shape.
var circuitNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// ManagesSSHConfig reports whether drift should write to the user's
// ~/.ssh/config + ~/.config/drift/ssh_config. It defaults to true when the
// field is absent, matching plans/PLAN.md § Client config layout.
func (c *Client) ManagesSSHConfig() bool {
	if c.ManageSSHConfig == nil {
		return true
	}
	return *c.ManageSSHConfig
}

// Validate enforces the schema invariants that the YAML shape alone cannot
// express: non-empty SSH target per circuit, circuit-name syntax, and that
// any default_circuit refers to a real circuit.
func (c *Client) Validate() error {
	for name, circuit := range c.Circuits {
		if !circuitNameRE.MatchString(name) {
			return fmt.Errorf("config: circuit name %q invalid (must match %s)", name, circuitNameRE.String())
		}
		if circuit.Host == "" {
			return fmt.Errorf("config: circuit %q: host is required", name)
		}
	}
	if c.DefaultCircuit != "" {
		if _, ok := c.Circuits[c.DefaultCircuit]; !ok {
			return fmt.Errorf("config: default_circuit %q is not defined under circuits", c.DefaultCircuit)
		}
	}
	return nil
}

// LoadClient decodes a client config from path. Missing files are not an
// error; the returned Client is the zero value (empty circuits, SSH config
// management enabled by default).
func LoadClient(path string) (*Client, error) {
	var c Client
	found, err := loadYAMLStrict(path, &c)
	if err != nil {
		return nil, err
	}
	if !found {
		return &c, nil
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// SaveClient atomically writes c to path after validating it. Parent
// directories are created with mode 0755 if absent; the file itself is 0600
// since it records SSH usernames and hostnames.
func SaveClient(path string, c *Client) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return marshalAndWrite(path, c, 0o600)
}
