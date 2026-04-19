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
