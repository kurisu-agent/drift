// Package systemd wraps the systemctl --user user-unit surface so lakitu
// can enable, disable, and query kart autostart units without open-coding
// argv construction at every call site.
//
// Only the subset of systemctl needed by plans/PLAN.md § "Auto-start on
// reboot" is exposed. Every command runs through internal/exec.Run so the
// Cancel/WaitDelay discipline is uniform with the rest of the codebase.
package systemd

import (
	"context"
	"errors"
	"fmt"
	"strings"

	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

// UnitTemplate is the systemd template unit name for per-kart autostart.
// The `@` suffix lets one template cover every kart; the `%i` instance
// token is substituted with the kart name by systemd at enable time.
const UnitTemplate = "lakitu-kart@"

// UnitFor returns the instantiated unit name for a kart.
func UnitFor(kart string) string { return UnitTemplate + kart + ".service" }

// Client is the small handle carrying the systemctl binary path. A zero
// value (empty Binary) resolves to "systemctl" on PATH, which is what
// production uses; tests inject a stub path.
type Client struct {
	Binary string
	// Runner is an optional override for the subprocess layer — handy in
	// tests. When nil, systemctl is invoked via internal/exec.Run.
	Runner func(ctx context.Context, cmd driftexec.Cmd) (driftexec.Result, error)
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
		return c.Runner(ctx, cmd)
	}
	return driftexec.Run(ctx, cmd)
}

// Enable runs `systemctl --user enable --now <unit>` for the kart. It is
// idempotent: systemctl exits 0 when the unit is already enabled + active.
func (c *Client) Enable(ctx context.Context, kart string) error {
	_, err := c.run(ctx, []string{"--user", "enable", "--now", UnitFor(kart)})
	return c.wrap(err, "enable", kart)
}

// Disable runs `systemctl --user disable --now <unit>`. Idempotent for the
// same reason as Enable — systemctl returns 0 on a unit that's already
// disabled + inactive.
func (c *Client) Disable(ctx context.Context, kart string) error {
	_, err := c.run(ctx, []string{"--user", "disable", "--now", UnitFor(kart)})
	return c.wrap(err, "disable", kart)
}

// IsEnabled reports whether the kart's unit is currently enabled. The
// underlying `systemctl is-enabled` exits non-zero for any state other than
// "enabled" (including "disabled", "static", "masked", and "not-found"), so
// we branch on the stdout token rather than the exit code.
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

// DenialError is returned when systemctl reports a permission-level failure
// (lingering not enabled, no $XDG_RUNTIME_DIR, etc.). Callers can map this
// to plans/PLAN.md's `code:6 systemd_denied` without re-parsing stderr.
type DenialError struct {
	Op     string
	Kart   string
	Stderr string
}

func (e *DenialError) Error() string {
	return fmt.Sprintf("systemctl --user %s %s denied: %s", e.Op, e.Kart, e.Stderr)
}

// wrap classifies an exec error into either a DenialError (for the
// user-session-not-active class of failures) or a generic wrapped error so
// callers can decide whether to surface a permissions message.
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

// looksLikeDenial is a narrow heuristic for systemctl's permission-class
// failure messages. We keep it small and explicit so a substring match
// doesn't swallow unrelated failures — expand as we see new phrasings.
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
