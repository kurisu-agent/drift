// Package systemd wraps the `systemctl --user` subset lakitu needs for
// per-kart autostart units. Everything routes through internal/exec.Run so
// the Cancel/WaitDelay discipline is uniform.
package systemd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

// UnitTemplate's `@` lets one template cover every kart; systemd
// substitutes `%i` with the kart name at enable time.
const UnitTemplate = "lakitu-kart@"

func UnitFor(kart string) string { return UnitTemplate + kart + ".service" }

type Client struct {
	Binary string
	// Runner overrides the subprocess layer for tests. Nil uses
	// driftexec.DefaultRunner.
	Runner driftexec.Runner
}

func (c *Client) bin() string {
	if c.Binary == "" {
		return "systemctl"
	}
	return c.Binary
}

func (c *Client) run(ctx context.Context, args []string) (driftexec.Result, error) {
	cmd := driftexec.Cmd{Name: c.bin(), Args: args}
	if c.Runner != nil {
		return c.Runner.Run(ctx, cmd)
	}
	return driftexec.DefaultRunner.Run(ctx, cmd)
}

func (c *Client) Enable(ctx context.Context, kart string) error {
	_, err := c.run(ctx, []string{"--user", "enable", "--now", UnitFor(kart)})
	return c.wrap(err, "enable", kart)
}

func (c *Client) Disable(ctx context.Context, kart string) error {
	_, err := c.run(ctx, []string{"--user", "disable", "--now", UnitFor(kart)})
	return c.wrap(err, "disable", kart)
}

// IsEnabled branches on stdout rather than exit code: `systemctl is-enabled`
// exits non-zero for every non-"enabled" state including disabled/static/
// masked/not-found, which we treat as false rather than error.
func (c *Client) IsEnabled(ctx context.Context, kart string) (bool, error) {
	res, err := c.run(ctx, []string{"--user", "is-enabled", UnitFor(kart)})
	state := strings.TrimSpace(string(res.Stdout))
	if state == "enabled" || state == "enabled-runtime" {
		return true, nil
	}
	if state == "disabled" || state == "masked" || state == "static" || state == "not-found" {
		return false, nil
	}
	if err != nil {
		return false, c.wrap(err, "is-enabled", kart)
	}
	return false, nil
}

// DenialError covers permission-class failures (no user bus, lingering
// disabled, etc.) so callers can map to `code:6 systemd_denied` without
// re-parsing stderr.
type DenialError struct {
	Op     string
	Kart   string
	Stderr string
}

func (e *DenialError) Error() string {
	return fmt.Sprintf("systemctl --user %s %s denied: %s", e.Op, e.Kart, e.Stderr)
}

func (c *Client) wrap(err error, op, kart string) error {
	if err == nil {
		return nil
	}
	var ee *driftexec.Error
	if errors.As(err, &ee) {
		stderr := strings.TrimSpace(string(ee.Stderr))
		if looksLikeDenial(stderr) {
			return &DenialError{Op: op, Kart: kart, Stderr: ee.FirstStderrLine}
		}
	}
	return fmt.Errorf("systemctl --user %s %s: %w", op, kart, err)
}

// looksLikeDenial is narrow on purpose — a loose substring match would
// swallow unrelated failures. Add phrasings as new ones appear.
func looksLikeDenial(stderr string) bool {
	needles := []string{
		"Failed to connect to bus",
		"Failed to connect to user bus",
		"$XDG_RUNTIME_DIR",
		"user lingering",
		"linger is not enabled",
		"Permission denied",
	}
	for _, n := range needles {
		if strings.Contains(stderr, n) {
			return true
		}
	}
	return false
}
