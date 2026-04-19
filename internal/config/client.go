package config

import (
	"fmt"
	"regexp"
)

type Client struct {
	DefaultCircuit  string                   `yaml:"default_circuit"`
	ManageSSHConfig *bool                    `yaml:"manage_ssh_config,omitempty"`
	Circuits        map[string]ClientCircuit `yaml:"circuits,omitempty"`
}

type ClientCircuit struct {
	Host string `yaml:"host"`
}

// Circuit names share the kart-name shape.
var circuitNameRE = regexp.MustCompile(`^[a-z][a-z0-9-]{0,62}$`)

// ManagesSSHConfig defaults to true when the field is absent.
func (c *Client) ManagesSSHConfig() bool {
	if c.ManageSSHConfig == nil {
		return true
	}
	return *c.ManageSSHConfig
}

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
