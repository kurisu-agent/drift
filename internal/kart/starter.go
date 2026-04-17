package kart

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

// fallbackAuthorName is the starter commit author when no character is
// attached. plans/PLAN.md § Starter history strip: "falls back to
// drift <noreply@drift.local> when no character is configured".
const fallbackAuthorName = "drift"
const fallbackAuthorEmail = "noreply@drift.local"

// StarterRunner is the execution seam used by the starter helpers. Matches
// internal/exec.Runner shape so [driftexec.Run] is the production default.
// Tests substitute a fake that records argv without touching git on disk.
type StarterRunner interface {
	Run(ctx context.Context, cmd driftexec.Cmd) (driftexec.Result, error)
}

// starterRunnerFunc adapts a plain function to [StarterRunner].
type starterRunnerFunc func(ctx context.Context, cmd driftexec.Cmd) (driftexec.Result, error)

func (f starterRunnerFunc) Run(ctx context.Context, cmd driftexec.Cmd) (driftexec.Result, error) {
	return f(ctx, cmd)
}

// defaultStarterRunner is the production starter runner — a thin pass
// through to internal/exec.Run.
var defaultStarterRunner StarterRunner = starterRunnerFunc(driftexec.Run)

// Starter runs the history-strip flow described in plans/PLAN.md § Starter
// history strip: clone → rm .git → git init → add+commit with character as
// author. The final directory path is what kart.new passes to `devpod up`
// as the positional source argument.
type Starter struct {
	Runner StarterRunner
}

// NewStarter returns a Starter that uses [driftexec.Run] under the hood.
func NewStarter() *Starter { return &Starter{Runner: defaultStarterRunner} }

// Strip clones url into destDir and rewrites the history so only a single
// "Initial commit from starter <url>" exists, authored by char. destDir
// must be empty or not yet exist; the caller owns tmpdir cleanup on error.
//
// This function never runs a shell — every step is a separate argv invoked
// through internal/exec, per plans/PLAN.md § Critical invariants.
func (s *Starter) Strip(ctx context.Context, url, destDir string, char *Character) error {
	if url == "" {
		return fmt.Errorf("starter: url is required")
	}
	if destDir == "" {
		return fmt.Errorf("starter: destDir is required")
	}
	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		return fmt.Errorf("starter: mkdir parent: %w", err)
	}

	runner := s.Runner
	if runner == nil {
		runner = defaultStarterRunner
	}

	// 1. git clone <url> <destDir>
	if _, err := runner.Run(ctx, driftexec.Cmd{
		Name: "git",
		Args: []string{"clone", "--", url, destDir},
	}); err != nil {
		return fmt.Errorf("starter: clone %s: %w", url, err)
	}

	// 2. rm -rf <destDir>/.git — use Go rather than shelling out to rm.
	gitDir := filepath.Join(destDir, ".git")
	if err := os.RemoveAll(gitDir); err != nil {
		return fmt.Errorf("starter: remove .git: %w", err)
	}

	// 3. git init inside the stripped tree.
	if _, err := runner.Run(ctx, driftexec.Cmd{
		Name: "git",
		Args: []string{"-C", destDir, "init"},
	}); err != nil {
		return fmt.Errorf("starter: init: %w", err)
	}

	// Configure author for the upcoming commit at repo scope only. Passing
	// via -c on the commit itself keeps the environment clean and avoids
	// touching the user's global git config.
	name, email := authorFor(char)
	commitEnv := []string{
		"GIT_AUTHOR_NAME=" + name,
		"GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name,
		"GIT_COMMITTER_EMAIL=" + email,
	}
	// git wants HOME set even with env vars; inherit it from the parent
	// along with PATH so git's config lookups work on tight sandboxes.
	commitEnv = append(commitEnv, inheritedEnv("HOME", "PATH", "USER")...)

	// 4. git add .
	if _, err := runner.Run(ctx, driftexec.Cmd{
		Name: "git",
		Args: []string{"-C", destDir, "add", "."},
		Env:  commitEnv,
	}); err != nil {
		return fmt.Errorf("starter: add: %w", err)
	}

	// 5. git commit -m "Initial commit from starter <url>"
	msg := fmt.Sprintf("Initial commit from starter %s", url)
	if _, err := runner.Run(ctx, driftexec.Cmd{
		Name: "git",
		Args: []string{"-C", destDir, "commit", "-m", msg, "--allow-empty"},
		Env:  commitEnv,
	}); err != nil {
		return fmt.Errorf("starter: commit: %w", err)
	}
	return nil
}

// authorFor picks the commit author. When the character has both fields
// populated it wins; otherwise fall back to drift <noreply@drift.local>.
func authorFor(c *Character) (string, string) {
	if c != nil && c.GitName != "" && c.GitEmail != "" {
		return c.GitName, c.GitEmail
	}
	return fallbackAuthorName, fallbackAuthorEmail
}

// inheritedEnv returns `K=V` entries for each named var pulled from the
// current process. Missing vars are skipped so the callee never sees an
// empty K= entry.
func inheritedEnv(keys ...string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}
