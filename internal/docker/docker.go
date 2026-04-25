// Package docker is lakitu's thin wrapper around the docker CLI for the
// hot paths where we need the daemon's view of every container in a
// single round-trip — kart.list and server.status both look up status
// for many workspaces at once. devpod's per-workspace `devpod status`
// shells out at ~1s each; one `docker ps` covers everything in ~15ms.
//
// Only the read-only inspection surface lives here. devpod still owns
// up/stop/delete/etc. — this package never mutates.
package docker

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"

	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

const DefaultBinary = "docker"

// Client routes every docker spawn through [internal/exec] so context
// cancellation and SIGTERM/SIGKILL escalation match the rest of lakitu.
// The zero value is usable.
type Client struct {
	Binary string
	Runner driftexec.Runner
}

func (c *Client) binary() string {
	if c == nil || c.Binary == "" {
		return DefaultBinary
	}
	return c.Binary
}

func (c *Client) runner() driftexec.Runner {
	if c == nil || c.Runner == nil {
		return driftexec.DefaultRunner
	}
	return c.Runner
}

// StatusByLabel returns the docker State of every container that carries
// the given label, keyed by the label's value. Used to fan in workspace
// status in a single docker round-trip — devpod sets the
// `dev.containers.id` label to the workspace UID, so callers index this
// map by workspace.UID.
//
// When several containers share the same label value (e.g. a recreate
// left an exited shell behind), the entry with the highest priority
// state wins so a still-running container isn't masked by its dead
// predecessor: running > restarting > paused > created > exited > dead.
//
// Missing keys mean "no container with that label" — typically a
// workspace that has never been started, or whose container has been
// pruned. Callers should treat that as stopped.
func (c *Client) StatusByLabel(ctx context.Context, label string) (map[string]string, error) {
	if label == "" {
		return nil, errors.New("docker: StatusByLabel: label is required")
	}
	res, err := c.runner().Run(ctx, driftexec.Cmd{
		Name: c.binary(),
		Args: []string{
			"ps", "-a",
			"--filter", "label=" + label,
			"--format", fmt.Sprintf(`{{.Label %q}} {{.State}}`, label),
		},
	})
	if err != nil {
		return nil, fmt.Errorf("docker ps: %w", err)
	}
	out := make(map[string]string)
	for _, line := range strings.Split(string(bytes.TrimSpace(res.Stdout)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, state, ok := strings.Cut(line, " ")
		if !ok || key == "" {
			continue
		}
		state = strings.TrimSpace(state)
		if existing, ok := out[key]; ok && statePriority(existing) >= statePriority(state) {
			continue
		}
		out[key] = state
	}
	return out, nil
}

// statePriority orders docker container states so the "most alive" entry
// wins when a label is duplicated across containers. Higher = preferred.
// Unknown states sort below all known ones so a typo never masks a real
// running container.
func statePriority(state string) int {
	switch strings.ToLower(strings.TrimSpace(state)) {
	case "running":
		return 6
	case "restarting":
		return 5
	case "paused":
		return 4
	case "created":
		return 3
	case "exited":
		return 2
	case "dead":
		return 1
	default:
		return 0
	}
}
