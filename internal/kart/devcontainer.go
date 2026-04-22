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

// NormalizeDevcontainerWithMounts is NormalizeDevcontainer plus a mount
// overlay. Behavior by input:
//
//   - raw == "" && len(mounts) == 0 → empty path, no file written.
//   - raw == "" && mounts != nil   → synthesize {"mounts": [...]} to tmpDir.
//   - raw != "" && len(mounts) == 0 → same as NormalizeDevcontainer.
//   - raw != "" && mounts != nil   → read/parse raw as JSONC (via hujson),
//     splice mounts into it (appended, deduped-by-target inside our input
//     only), serialize as strict JSON to tmpDir. devpod's own
//     mergeMounts dedups again against the project's devcontainer.json at
//     merge time.
//
// In all mount-bearing paths the file lands in tmpDir and cleanup removes
// it; callers wire cleanup into the kart.new defer chain.
func NormalizeDevcontainerWithMounts(
	ctx context.Context,
	raw, tmpDir string,
	mounts []model.Mount,
	fetch DevcontainerFetcher,
) (path string, cleanup func(), err error) {
	cleanup = func() {}
	raw = strings.TrimSpace(raw)
	if len(mounts) == 0 {
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

	spliced, serr := spliceMounts(baseBody, mounts)
	if serr != nil {
		return "", cleanup, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"kart.new: splice mount_dirs: %v", serr)
	}
	return writeDevcontainerFile(tmpDir, spliced)
}

// spliceMounts parses body as JSONC (hujson-tolerant), appends mounts to
// its `mounts` array (creating one if absent), and returns a strict-JSON
// serialization. Existing mounts in body are kept; our mounts are appended
// after any with a matching target removed from the body's list. That
// local dedup keeps the overlay file tidy; devpod's own mergeMounts does
// the same against the project's devcontainer.json at merge time.
func spliceMounts(body []byte, mounts []model.Mount) ([]byte, error) {
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

	existing, _ := root["mounts"].([]any)
	incomingTargets := make(map[string]bool, len(mounts))
	for _, m := range mounts {
		if m.Target != "" {
			incomingTargets[m.Target] = true
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
		kept = append(kept, mountToMap(m))
	}
	root["mounts"] = kept

	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal overlay: %w", err)
	}
	return append(out, '\n'), nil
}

func mountToMap(m model.Mount) map[string]any {
	out := map[string]any{}
	if m.Type != "" {
		out["type"] = m.Type
	}
	if m.Source != "" {
		out["source"] = m.Source
	}
	if m.Target != "" {
		out["target"] = m.Target
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
