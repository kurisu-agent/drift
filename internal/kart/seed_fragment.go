package kart

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tailscale/hujson"

	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/seed"
)

// seedFragments renders every seed template attached to the resolved
// kart and emits one shell snippet per file. Empty when no templates
// resolved (the common path for tunes without a `seed:` list). Each
// file is base64-pinned so user-supplied templates can't escape into
// arbitrary shell on the circuit.
//
// Merge-mode files require the existing in-container content to merge
// against. We issue one extra `devpod ssh` per merge file to fetch it
// (the merge itself runs in lakitu), then collapse the result to a
// plain overwrite for the final write batch — keeping the contract
// sandboxed without coupling karts to jq/yq/python.
func seedFragments(ctx context.Context, dp *devpod.Client, r *Resolved) (string, error) {
	if r == nil || r.Name == "" || len(r.Seeds) == 0 {
		return "", nil
	}

	vars := kartVars(r)

	var b strings.Builder
	for _, t := range r.Seeds {
		files, err := seed.Render(t, vars)
		if err != nil {
			return "", fmt.Errorf("seed %q: %w", t.Name, err)
		}
		for _, f := range files {
			frag, err := renderSeedFile(ctx, dp, r.Name, t.Name, f)
			if err != nil {
				return "", err
			}
			b.WriteString(frag)
		}
	}
	return b.String(), nil
}

// renderSeedFile turns one rendered seed file into the shell that
// drops it into the kart, dispatching on the conflict mode. Merge does
// the read+merge here (so the emitted shell stays a plain write);
// every other mode is pure shell.
func renderSeedFile(ctx context.Context, dp *devpod.Client, kart, tmpl string, f seed.RenderedFile) (string, error) {
	mode := f.OnConflict
	if mode == "" {
		mode = seed.ConflictOverwrite
	}
	if mode != seed.ConflictMerge {
		return seedFileFragment(f, mode), nil
	}

	format, err := seed.MergeFormatFromPath(f.Path)
	if err != nil {
		return "", fmt.Errorf("seed %q file %q: %w", tmpl, f.Path, err)
	}
	existing, err := readKartFile(ctx, dp, kart, expandHome(f.Path))
	if err != nil {
		return "", fmt.Errorf("seed %q file %q: read existing: %w", tmpl, f.Path, err)
	}
	merged, err := seed.Merge(format, existing, f.Content)
	if err != nil {
		return "", fmt.Errorf("seed %q file %q: merge: %w", tmpl, f.Path, err)
	}
	// Once merged, the write is a plain overwrite — the existing
	// content has already been folded into `merged`.
	f.Content = merged
	return seedFileFragment(f, seed.ConflictOverwrite), nil
}

// readKartFile fetches the bytes of a single file from inside the kart
// via `devpod ssh --command 'cat …'`. A missing file returns empty
// bytes and no error so merge can degrade to "first write" cleanly.
func readKartFile(ctx context.Context, dp *devpod.Client, kart, path string) ([]byte, error) {
	if dp == nil {
		return nil, fmt.Errorf("kart.new: devpod client is nil (caller wiring bug)")
	}
	// %q double-quotes the path so a leading `$HOME/` expands at the
	// remote shell — single-quoting would suppress that. `$` is one of
	// the few bytes Go's %q doesn't escape, so the variable survives
	// the round trip. `|| true` swallows cat's nonzero exit when the
	// file is missing; `2>/dev/null` keeps stderr quiet either way.
	cmd := fmt.Sprintf("cat %q 2>/dev/null || true", path)
	out, err := dp.SSH(ctx, devpod.SSHOpts{Name: kart, Command: cmd})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// seedFileFragment turns one rendered file into the shell that drops
// it into the container, given the resolved conflict mode. `~/`-
// prefixed paths expand via $HOME at run time. `merge` is not handled
// here — the caller has already collapsed it to `overwrite`.
func seedFileFragment(f seed.RenderedFile, mode seed.ConflictMode) string {
	dst := expandHome(f.Path)
	dir := shellDirname(dst)

	var b strings.Builder
	switch mode {
	case seed.ConflictSkip:
		fmt.Fprintf(&b, "if [ ! -e %q ]; then\n", dst)
		writeBody(&b, dst, dir, f, "")
		b.WriteString("fi\n")

	case seed.ConflictAppend:
		writeBody(&b, dst, dir, f, ">>")

	case seed.ConflictPrepend:
		// New content is base64-decoded into a sibling temp; the
		// existing file (if any) is appended; rename swaps atomically.
		tmp := dst + ".seed-prepend.tmp"
		if dir != "" && dir != "." {
			fmt.Fprintf(&b, "mkdir -p %q\n", dir)
		}
		if f.BreakSymlinks {
			fmt.Fprintf(&b, "if [ -L %q ]; then rm -f %q; fi\n", dst, dst)
		}
		fmt.Fprintf(&b, "%s\n", base64DecodeStmt(tmp, f.Content))
		fmt.Fprintf(&b, "if [ -e %q ]; then cat %q >> %q; fi\n", dst, dst, tmp)
		fmt.Fprintf(&b, "mv %q %q\n", tmp, dst)

	default: // overwrite (also covers an unset mode)
		writeBody(&b, dst, dir, f, "")
	}
	return b.String()
}

// writeBody emits the canonical mkdir/symlink-break/base64-write block
// for overwrite + append. `redirect` is "" for overwrite (`>`) or
// ">>" for append.
func writeBody(b *strings.Builder, dst, dir string, f seed.RenderedFile, redirect string) {
	if dir != "" && dir != "." {
		fmt.Fprintf(b, "mkdir -p %q\n", dir)
	}
	fmt.Fprintf(b, "dst=%q\n", dst)
	if f.BreakSymlinks {
		b.WriteString(`if [ -L "$dst" ]; then rm -f "$dst"; fi` + "\n")
	}
	if redirect == "" {
		b.WriteString(base64WriteStmt(`$dst`, f.Content))
	} else {
		b.WriteString(base64AppendStmt(`$dst`, f.Content))
	}
}

// expandHome rewrites a leading `~/` to a `$HOME/` form for the shell.
// Anything else (absolute, $-rooted, etc.) passes through verbatim.
func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		return "$HOME/" + strings.TrimPrefix(path, "~/")
	}
	return path
}

// shellDirname mirrors filepath.Dir but operates on the post-expansion
// shell-form path so the `mkdir -p` and the `printf >` target line up.
func shellDirname(path string) string {
	i := strings.LastIndex(path, "/")
	if i < 0 {
		return ""
	}
	return path[:i]
}

// kartVars assembles the substitution context for seed templates from
// the resolved kart. Fixed keys for now; CLI-passed vars would layer
// in here without disturbing the seed package's contract.
func kartVars(r *Resolved) seed.Vars {
	image, relPath := probeProjectDevcontainer(r.Name)
	if relPath == "" {
		relPath = ".devcontainer/devcontainer.json"
	}
	workspace := "/workspaces/" + r.Name
	return seed.Vars{
		"Kart":             r.Name,
		"Workspace":        workspace,
		"Image":            image,
		"DevcontainerPath": workspace + "/" + relPath,
	}
}

// probeProjectDevcontainer reads the cloned repo's devcontainer.json
// (looking in the two standard locations) and extracts the image
// field. Best-effort: any missing file or parse failure returns
// empty strings, which templates handle as "unknown".
func probeProjectDevcontainer(kart string) (image, relPath string) {
	dir := filepath.Join(devpod.AgentContextsRoot(), "default", "workspaces", kart, "content")
	for _, candidate := range []string{".devcontainer/devcontainer.json", ".devcontainer.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, candidate))
		if err != nil {
			continue
		}
		standard, err := hujson.Standardize(raw)
		if err != nil {
			return "", candidate
		}
		var dc struct {
			Image string `json:"image"`
		}
		if err := json.Unmarshal(standard, &dc); err != nil {
			return "", candidate
		}
		return dc.Image, candidate
	}
	return "", ""
}
