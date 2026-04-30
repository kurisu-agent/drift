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

// Clone clones url into destDir, preserving git history (no
// rm-rf/.git step). When the kart's resolved character carries a PAT
// and url is a github HTTPS URL, the helper injects the token as
// basic-auth for the clone command, then rewrites the remote URL to
// the bare form so the token doesn't persist in destDir/.git/config —
// subsequent pulls/pushes inside the kart fall through to the
// git_credentials helper that layer-1 dotfiles installs.
//
// Why server-side: this devpod fork's source-URL parser splits the
// `--source` arg on `@` and treats anything after as a branch ref, so
// passing devpod a URL with `x-access-token:TOKEN@github.com` butchers
// the URL into URL=`https://x-access-token:TOKEN`,
// branch=`github.com/owner/repo`. Pre-cloning here and handing devpod
// a local path sidesteps the parser entirely (upstream rejected the
// fix in loft-sh/devpod#885 as not-planned).
//
// Idempotence: destDir is RemoveAll'd before and after a failed clone
// so a leftover from a partial run can't trip git's "destination not
// empty" check on the retry. Errors carry the redacted stderr tail
// from git so the operator sees the real cause (auth failure, network,
// etc.) rather than the generic "exited 128" surface.
func (s *Starter) Clone(ctx context.Context, url, destDir string, char *Character) error {
	if url == "" {
		return fmt.Errorf("clone: url is required")
	}
	if destDir == "" {
		return fmt.Errorf("clone: destDir is required")
	}
	if err := os.MkdirAll(filepath.Dir(destDir), 0o700); err != nil {
		return fmt.Errorf("clone: mkdir parent: %w", err)
	}
	// Defensive: clear any leftover from an earlier partial run so git
	// doesn't refuse with "destination path 'X' already exists and is
	// not an empty directory."
	if err := os.RemoveAll(destDir); err != nil {
		return fmt.Errorf("clone: clear destDir: %w", err)
	}

	runner := s.Runner
	if runner == nil {
		runner = driftexec.DefaultRunner
	}

	cloneURL := url
	if char != nil && char.PAT != "" {
		if rewritten, ok := injectGithubPATIntoCloneURL(url, char.PAT); ok {
			cloneURL = rewritten
		}
	}

	if _, err := runner.Run(ctx, driftexec.Cmd{
		Name: "git",
		Args: []string{"clone", "--", cloneURL, destDir},
	}); err != nil {
		// Clean up the partial-clone tree so a retry doesn't double-fail
		// on the dir-already-exists check above.
		_ = os.RemoveAll(destDir)
		// Surface the actual git error (last stderr lines, redacted by
		// driftexec) instead of just the first progress line that
		// driftexec.Error.Error() returns.
		if tail := driftexec.StderrTail(err); tail != "" {
			return fmt.Errorf("clone %s: %s", url, tail)
		}
		return fmt.Errorf("clone %s: %w", url, err)
	}

	if cloneURL != url {
		if _, err := runner.Run(ctx, driftexec.Cmd{
			Name: "git",
			Args: []string{"-C", destDir, "remote", "set-url", "origin", url},
		}); err != nil {
			_ = os.RemoveAll(destDir)
			if tail := driftexec.StderrTail(err); tail != "" {
				return fmt.Errorf("clone: rewrite remote: %s", tail)
			}
			return fmt.Errorf("clone: rewrite remote: %w", err)
		}
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
