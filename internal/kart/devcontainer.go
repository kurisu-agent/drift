package kart

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/tailscale/hujson"

	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// DevcontainerFetcher downloads a devcontainer.json. Tests substitute a
// fake so no network is required.
type DevcontainerFetcher func(ctx context.Context, url string) ([]byte, error)

func defaultDevcontainerFetcher(ctx context.Context, url string) ([]byte, error) {
	cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, fmt.Errorf("devcontainer: build request: %w", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("devcontainer: fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("devcontainer: fetch %s: %s", url, resp.Status)
	}
	// 1 MiB is well past a reasonable devcontainer.json; anything larger
	// is almost certainly a misconfigured URL.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("devcontainer: read %s: %w", url, err)
	}
	return body, nil
}

// NormalizeDevcontainer turns raw — a file path, JSON literal, or URL —
// into a path for devpod's --extra-devcontainer-path. The returned cleanup
// is safe to call even when no temp file was written.
func NormalizeDevcontainer(ctx context.Context, raw, tmpDir string, fetch DevcontainerFetcher) (path string, cleanup func(), err error) {
	cleanup = func() {}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", cleanup, nil
	}
	if fetch == nil {
		fetch = defaultDevcontainerFetcher
	}

	switch {
	case strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://"):
		body, err := fetch(ctx, raw)
		if err != nil {
			return "", cleanup, rpcerr.UserError(rpcerr.TypeInvalidFlag,
				"kart.new: --devcontainer: %v", err)
		}
		return writeDevcontainerFile(tmpDir, body)

	case strings.HasPrefix(raw, "{"):
		// Validate before writing — catching a typo here beats a mid-build
		// devpod error.
		var probe any
		if err := json.Unmarshal([]byte(raw), &probe); err != nil {
			return "", cleanup, rpcerr.UserError(rpcerr.TypeInvalidFlag,
				"kart.new: --devcontainer is not valid JSON: %v", err)
		}
		return writeDevcontainerFile(tmpDir, []byte(raw))

	default:
		// File path — don't require existence here; the file may be
		// unreadable or mount-pending and devpod surfaces a clearer error.
		if raw == "" {
			return "", cleanup, nil
		}
		return raw, cleanup, nil
	}
}

// Overlay is the set of additions lakitu splices into the kart's
// devcontainer.json at kart.new time. Zero-value is a valid empty
// overlay (nothing to splice).
type Overlay struct {
	// Mounts is appended to the base devcontainer's `mounts` array,
	// deduped by target inside this call. Targets with a leading
	// `~/` rewrite to /mnt/lakitu-host/<rest> here; post-`devpod up`
	// the container's $HOME is symlinked onto that tree.
	Mounts []model.Mount
}

// empty reports whether the overlay adds nothing. Short-circuits the
// file-write path so callers that pass a zero Overlay behave identically
// to the no-overlay NormalizeDevcontainer path.
func (o Overlay) empty() bool {
	return len(o.Mounts) == 0
}

// NormalizeDevcontainerWithOverlay is NormalizeDevcontainer plus an
// overlay (mounts and/or user-normalisation fields). Behavior by input:
//
//   - raw == "" && overlay.empty() → empty path, no file written.
//   - raw == "" && !empty          → synthesize {"mounts":[...],"remoteUser":...}.
//   - raw != "" && overlay.empty() → same as NormalizeDevcontainer.
//   - raw != "" && !empty          → read/parse raw as JSONC (via hujson),
//     splice overlay into it, serialize as strict JSON to tmpDir. devpod's
//     own mergeMounts dedups mounts again against the project's
//     devcontainer.json at merge time.
//
// In all overlay-bearing paths the file lands in tmpDir and cleanup
// removes it; callers wire cleanup into the kart.new defer chain.
func NormalizeDevcontainerWithOverlay(
	ctx context.Context,
	raw, tmpDir string,
	overlay Overlay,
	fetch DevcontainerFetcher,
) (path string, cleanup func(), err error) {
	cleanup = func() {}
	raw = strings.TrimSpace(raw)
	if overlay.empty() {
		return NormalizeDevcontainer(ctx, raw, tmpDir, fetch)
	}
	if fetch == nil {
		fetch = defaultDevcontainerFetcher
	}

	var baseBody []byte
	switch {
	case raw == "":
		baseBody = []byte("{}")
	case strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://"):
		body, ferr := fetch(ctx, raw)
		if ferr != nil {
			return "", cleanup, rpcerr.UserError(rpcerr.TypeInvalidFlag,
				"kart.new: --devcontainer: %v", ferr)
		}
		baseBody = body
	case strings.HasPrefix(raw, "{"):
		baseBody = []byte(raw)
	default:
		body, rerr := os.ReadFile(raw)
		if rerr != nil {
			return "", cleanup, rpcerr.UserError(rpcerr.TypeInvalidFlag,
				"kart.new: --devcontainer: read %s: %v", raw, rerr)
		}
		baseBody = body
	}

	spliced, serr := spliceOverlay(baseBody, overlay)
	if serr != nil {
		return "", cleanup, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"kart.new: splice overlay: %v", serr)
	}
	return writeDevcontainerFile(tmpDir, spliced)
}

// spliceOverlay parses body as JSONC (hujson-tolerant), applies the
// overlay (mounts and/or user normalisation), and returns strict JSON.
// Existing mounts with a target that collides with an overlay mount are
// replaced; the overlay's onCreateCommand entry is merged into the
// object form so a project-authored one still runs.
func spliceOverlay(body []byte, overlay Overlay) ([]byte, error) {
	normalized, err := hujson.Standardize(body)
	if err != nil {
		return nil, fmt.Errorf("parse jsonc: %w", err)
	}
	var root map[string]any
	if err := json.Unmarshal(normalized, &root); err != nil {
		return nil, fmt.Errorf("devcontainer is not a JSON object: %w", err)
	}
	if root == nil {
		root = map[string]any{}
	}

	if len(overlay.Mounts) > 0 {
		spliceMountsInto(root, overlay.Mounts)
	}

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal overlay: %w", err)
	}
	return append(out, '\n'), nil
}

func spliceMountsInto(root map[string]any, mounts []model.Mount) {
	existing, _ := root["mounts"].([]any)
	incomingTargets := make(map[string]bool, len(mounts))
	for _, m := range mounts {
		if isCopyMount(m) {
			continue
		}
		if m.Target != "" {
			incomingTargets[rewriteTargetForSplice(m.Target)] = true
		}
	}
	kept := existing[:0:0]
	for _, raw := range existing {
		obj, ok := raw.(map[string]any)
		if !ok {
			kept = append(kept, raw)
			continue
		}
		target, _ := obj["target"].(string)
		if incomingTargets[target] {
			continue
		}
		kept = append(kept, raw)
	}
	for _, m := range mounts {
		if isCopyMount(m) {
			continue
		}
		kept = append(kept, mountToMap(m))
	}
	root["mounts"] = kept
}

// isCopyMount flags lakitu-only pseudo-mounts (`type: copy`), which
// never reach docker — they become file drops via copyFragment.
func isCopyMount(m model.Mount) bool { return m.Type == model.MountTypeCopy }

// hostMountPrefix is the in-container path `~/`-targeted mounts land
// at. Post-`devpod up` the image's default-user $HOME is symlinked
// onto this tree, so agents running as the image's default user see
// the host's content at `$HOME/<rest>`.
const hostMountPrefix = "/mnt/lakitu-host/"

// targetInHome reports whether the mount's target is a `~/`-form the
// post-up helper should symlink into the container's $HOME.
// Returns the suffix (e.g. ".claude" for target `~/.claude`). Bare
// `~` has suffix "".
func targetInHome(target string) (suffix string, ok bool) {
	switch {
	case target == "~":
		return "", true
	case strings.HasPrefix(target, "~/"):
		return target[2:], true
	default:
		return "", false
	}
}

// rewriteTargetForSplice expands a `~/<rest>` target to the lakitu
// host-mount path; non-home-rooted targets pass through untouched.
func rewriteTargetForSplice(target string) string {
	suffix, ok := targetInHome(target)
	if !ok {
		return target
	}
	return hostMountPrefix + suffix
}

func mountToMap(m model.Mount) map[string]any {
	out := map[string]any{}
	if m.Type != "" {
		out["type"] = m.Type
	}
	if m.Source != "" {
		out["source"] = expandHomeTildeSource(m.Source)
	}
	if m.Target != "" {
		out["target"] = rewriteTargetForSplice(m.Target)
	}
	if m.External {
		out["external"] = true
	}
	if len(m.Other) > 0 {
		other := make([]any, len(m.Other))
		for i, v := range m.Other {
			other[i] = v
		}
		out["other"] = other
	}
	return out
}

func writeDevcontainerFile(tmpDir string, body []byte) (string, func(), error) {
	cleanup := func() {}
	if tmpDir == "" {
		return "", cleanup, fmt.Errorf("devcontainer: tmpDir is required")
	}
	if err := os.MkdirAll(tmpDir, 0o700); err != nil {
		return "", cleanup, fmt.Errorf("devcontainer: mkdir %s: %w", tmpDir, err)
	}
	path := filepath.Join(tmpDir, "devcontainer.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", cleanup, fmt.Errorf("devcontainer: write %s: %w", path, err)
	}
	cleanup = func() { _ = os.Remove(path) }
	return path, cleanup, nil
}
