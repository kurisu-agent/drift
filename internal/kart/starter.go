package kart

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

const fallbackAuthorName = "drift"
const fallbackAuthorEmail = "noreply@drift.local"

// Starter runs the history-strip flow: clone → rm .git → git init →
// add+commit with character as author. The final directory path is what
// kart.new passes to `devpod up` as the positional source.
type Starter struct {
	Runner driftexec.Runner
}

func NewStarter() *Starter { return &Starter{Runner: driftexec.DefaultRunner} }

// Strip clones url into destDir and rewrites the history to a single
// "Initial commit from starter <url>" authored by char. destDir must be
// empty or not yet exist; the caller owns tmpdir cleanup.
func (s *Starter) Strip(ctx context.Context, url, destDir string, char *Character) error {
	if url == "" {
		return fmt.Errorf("starter: url is required")
	}
	if destDir == "" {
		return fmt.Errorf("starter: destDir is required")
	}
	if err := os.MkdirAll(filepath.Dir(destDir), 0o700); err != nil {
		return fmt.Errorf("starter: mkdir parent: %w", err)
	}

	runner := s.Runner
	if runner == nil {
		runner = driftexec.DefaultRunner
	}

	if _, err := runner.Run(ctx, driftexec.Cmd{
		Name: "git",
		Args: []string{"clone", "--", url, destDir},
	}); err != nil {
		return fmt.Errorf("starter: clone %s: %w", url, err)
	}

	gitDir := filepath.Join(destDir, ".git")
	if err := os.RemoveAll(gitDir); err != nil {
		return fmt.Errorf("starter: remove .git: %w", err)
	}

	if _, err := runner.Run(ctx, driftexec.Cmd{
		Name: "git",
		Args: []string{"-C", destDir, "init"},
	}); err != nil {
		return fmt.Errorf("starter: init: %w", err)
	}

	// Author via env keeps the commit clean of touching the user's global
	// git config. HOME/PATH/USER pass through so git's config lookups work
	// on tight sandboxes.
	name, email := authorFor(char)
	commitEnv := []string{
		"GIT_AUTHOR_NAME=" + name,
		"GIT_AUTHOR_EMAIL=" + email,
		"GIT_COMMITTER_NAME=" + name,
		"GIT_COMMITTER_EMAIL=" + email,
	}
	commitEnv = append(commitEnv, inheritedEnv("HOME", "PATH", "USER")...)

	if _, err := runner.Run(ctx, driftexec.Cmd{
		Name: "git",
		Args: []string{"-C", destDir, "add", "."},
		Env:  commitEnv,
	}); err != nil {
		return fmt.Errorf("starter: add: %w", err)
	}

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

func authorFor(c *Character) (string, string) {
	if c != nil && c.GitName != "" && c.GitEmail != "" {
		return c.GitName, c.GitEmail
	}
	return fallbackAuthorName, fallbackAuthorEmail
}

// inheritedEnv returns K=V entries for each named var present in the
// current process. Missing vars are skipped so the callee never sees K=.
func inheritedEnv(keys ...string) []string {
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	return out
}
