package drift

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/wire"
	"golang.org/x/sync/errgroup"
)

// deleteCmd errors on missing (unlike start/stop/restart); not_found
// flows through errfmt.Emit like any other rpcerr. Destructive, so it
// prompts on a TTY by default — pass -y to skip, which is the only way
// to run this in scripted / non-TTY contexts.
//
// Name accepts shell-style globs (`*`, `?`, `[abc]`) when it contains
// any of those metacharacters. Glob mode lists matching karts on the
// resolved circuit, prompts once with the matched names, then deletes
// them in parallel under a small sliding window.
type deleteCmd struct {
	Name  string `arg:"" optional:"" help:"Kart name or shell-style glob (e.g. 'test-*'); omit on a TTY to pick from a cross-circuit kart list."`
	Force bool   `short:"y" name:"yes" aliases:"force" help:"Skip the interactive confirmation prompt."`
}

func runKartDelete(ctx context.Context, io IO, root *CLI, cmd deleteCmd, deps deps) int {
	if isGlobPattern(cmd.Name) {
		return runKartDeleteGlob(ctx, io, root, cmd, deps)
	}
	circuit, name, ok, rc := resolveKartTarget(ctx, io, root, deps, cmd.Name, "drift delete")
	if !ok {
		return rc
	}
	if !cmd.Force {
		confirmed, err := confirmDelete(io, circuit, name)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if !confirmed {
			fmt.Fprintln(io.Stderr, "aborted")
			return 1
		}
	}
	return runKartLifecycleOn(ctx, io, root, circuit, name, wire.MethodKartDelete, "deleting", "deleted", deps)
}

// isGlobPattern reports whether s contains any shell-style glob
// metacharacter recognised by filepath.Match. Plain names fall through
// to the historical single-name path; only patterns with `*`, `?`, or
// `[` opt into the matched-name listing + parallel-delete flow.
func isGlobPattern(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// runKartDeleteGlob fetches kart.list on the resolved circuit, matches
// names with filepath.Match, prompts the user with the matched set, and
// fans the kart.delete RPCs out under a sliding window.
//
// Glob mode is single-circuit (the resolved default or -c override) —
// no cross-circuit picker. JSON output prints a per-name result array;
// text output prints one line per kart and a final summary on stderr.
func runKartDeleteGlob(ctx context.Context, io IO, root *CLI, cmd deleteCmd, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	entries, _, err := fetchKartList(ctx, deps, circuit)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	matches, err := matchKartGlob(cmd.Name, entries)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if len(matches) == 0 {
		fmt.Fprintf(io.Stderr, "no karts on circuit %q match %q\n", circuit, cmd.Name)
		return 1
	}

	if !cmd.Force {
		confirmed, cerr := confirmDeleteGlob(io, circuit, cmd.Name, matches)
		if cerr != nil {
			return errfmt.Emit(io.Stderr, cerr)
		}
		if !confirmed {
			fmt.Fprintln(io.Stderr, "aborted")
			return 1
		}
	}
	return deleteKartsParallel(ctx, io, root, deps, circuit, matches)
}

// matchKartGlob filters entries down to those whose Name matches pat.
// Returns names sorted for stable display. filepath.Match's only error
// is ErrBadPattern, which we surface verbatim so the user sees what
// they typed wrong.
func matchKartGlob(pat string, entries []listEntry) ([]string, error) {
	matched := make([]string, 0)
	for _, e := range entries {
		ok, err := filepath.Match(pat, e.Name)
		if err != nil {
			return nil, fmt.Errorf("invalid glob pattern %q: %w", pat, err)
		}
		if ok {
			matched = append(matched, e.Name)
		}
	}
	sort.Strings(matched)
	return matched, nil
}

// confirmDeleteGlob lists every matched kart so the user sees exactly
// what's about to be dropped — globs make it trivial to over-match, and
// a one-line preview is what makes this safer than `drift delete *` in
// a shell. Same non-TTY rule as confirmDelete.
func confirmDeleteGlob(io IO, circuit, pat string, names []string) (bool, error) {
	if !stdinIsTTY(io.Stdin) {
		return false, errors.New("drift kart delete requires -y on non-interactive stdin")
	}
	p := style.For(io.Stderr, false)
	fmt.Fprintf(io.Stderr, "%s pattern %q matches %d kart(s) on circuit %q:\n",
		p.Warn("!"), pat, len(names), circuit)
	for _, n := range names {
		fmt.Fprintf(io.Stderr, "    %s\n", n)
	}
	fmt.Fprintf(io.Stderr, "delete all %d? [y/N]: ", len(names))
	br := bufio.NewReader(io.Stdin)
	line, err := br.ReadString('\n')
	if err != nil {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// globDeleteConcurrency caps the in-flight kart.delete RPCs. devpod
// delete does real work (container teardown, garage cleanup) and
// flooding lakitu with N=20 parallel deletes thrashes the host;
// 4 is in line with the kart.list fanout in collectCircuitKarts.
const globDeleteConcurrency = 4

// globDeleteResult is the per-name outcome surfaced in JSON output and
// the text summary. Status mirrors kart.delete's response shape on
// success; failures carry the error string and skip Status.
type globDeleteResult struct {
	Name   string `json:"name"`
	Status string `json:"status,omitempty"`
	Error  string `json:"error,omitempty"`
}

// deleteKartsParallel runs kart.delete for each name with a sliding
// window. Per-RPC failures don't abort the batch — a half-deleted glob
// is worse than reporting which ones failed and letting the user
// retry. Returns 0 iff every kart deleted cleanly.
func deleteKartsParallel(ctx context.Context, io IO, root *CLI, deps deps, circuit string, names []string) int {
	results := make([]globDeleteResult, len(names))
	var eg errgroup.Group
	eg.SetLimit(globDeleteConcurrency)
	for i, n := range names {
		eg.Go(func() error {
			results[i].Name = n
			var raw json.RawMessage
			if err := deps.call(ctx, circuit, wire.MethodKartDelete, map[string]string{"name": n}, &raw); err != nil {
				results[i].Error = err.Error()
				return nil
			}
			var res struct {
				Status string `json:"status"`
			}
			if err := json.Unmarshal(raw, &res); err == nil {
				results[i].Status = res.Status
			}
			return nil
		})
	}
	_ = eg.Wait()

	if root != nil && root.Output == "json" {
		return emitJSON(io, struct {
			Circuit string             `json:"circuit"`
			Pattern string             `json:"pattern"`
			Results []globDeleteResult `json:"results"`
		}{Circuit: circuit, Results: results})
	}

	p := style.For(io.Stderr, false)
	failed := 0
	for _, r := range results {
		if r.Error != "" {
			failed++
			fmt.Fprintf(io.Stderr, "%s %s: %s\n", p.Warn("✗"), r.Name, r.Error)
			continue
		}
		fmt.Fprintf(io.Stdout, "deleted kart %q (status %s)\n", r.Name, r.Status)
	}
	if failed > 0 {
		fmt.Fprintf(io.Stderr, "deleted %d/%d (%d failed)\n", len(results)-failed, len(results), failed)
		return 1
	}
	fmt.Fprintf(io.Stderr, "deleted %d kart(s) on circuit %q\n", len(results), circuit)
	return 0
}

// confirmDelete returns (answer, err). Non-TTY stdin with no -y is a user
// error — silently aborting would hide the problem in CI logs, and auto-
// confirming would be unsafe. Includes the circuit so users coming out
// of the picker see exactly which (circuit, kart) they're about to drop.
func confirmDelete(io IO, circuit, name string) (bool, error) {
	if !stdinIsTTY(io.Stdin) {
		return false, errors.New("drift kart delete requires -y on non-interactive stdin")
	}
	p := style.For(io.Stderr, false)
	fmt.Fprintf(io.Stderr, "%s delete kart %q on circuit %q? [y/N]: ",
		p.Warn("!"), name, circuit)
	br := bufio.NewReader(io.Stdin)
	line, err := br.ReadString('\n')
	if err != nil {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}
