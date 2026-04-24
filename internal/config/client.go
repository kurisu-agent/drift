package config

import (
	"fmt"

	"github.com/kurisu-agent/drift/internal/name"
)

type Client struct {
	DefaultCircuit  string                   `yaml:"default_circuit"`
	ManageSSHConfig *bool                    `yaml:"manage_ssh_config,omitempty"`
	Circuits        map[string]ClientCircuit `yaml:"circuits,omitempty"`
}

type ClientCircuit struct {
	Host string `yaml:"host"`
	// SSH is a map of ssh_config directive names to values. Entries flow
	// straight into the generated `Host drift.<circuit>` block in
	// ~/.config/drift/ssh_config, so lakitu RPCs (which always dial
	// `ssh drift.<circuit>`) pick them up. Keys use ssh_config's native
	// names — IdentityFile, Port, ForwardAgent, User, ProxyJump, …
	// Values are not pre-expanded; `~/` resolves at ssh-use time.
	SSH map[string]string `yaml:"ssh,omitempty"`
}

// ManagesSSHConfig defaults to true when the field is absent.
func (c *Client) ManagesSSHConfig() bool {
	if c.ManageSSHConfig == nil {
		return true
	}
	return *c.ManageSSHConfig
}

func (c *Client) Validate() error {
	for circuitName, circuit := range c.Circuits {
		if err := name.Validate("circuit", circuitName); err != nil {
			return fmt.Errorf("config: %w", err)
		}
		if circuit.Host == "" {
			return fmt.Errorf("config: circuit %q: host is required", circuitName)
		}
	}
	if c.DefaultCircuit != "" {
		if _, ok := c.Circuits[c.DefaultCircuit]; !ok {
			return fmt.Errorf("config: default_circuit %q is not defined under circuits", c.DefaultCircuit)
		}
	}
	return nil
}

// LoadClient: missing files return the zero-value Client, not an error.
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

// SaveClient writes 0600 — records SSH usernames and hostnames.
func SaveClient(path string, c *Client) error {
	if err := c.Validate(); err != nil {
		return err
	}
	return marshalAndWrite(path, c, 0o600)
}
