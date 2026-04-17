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

// DevcontainerFetcher downloads a devcontainer.json from a remote URL. The
// production fetcher uses net/http; tests substitute a fake so no network is
// required. The returned bytes are written verbatim to a temp file.
type DevcontainerFetcher func(ctx context.Context, url string) ([]byte, error)

// defaultDevcontainerFetcher is the production URL fetcher. Timeout is
// generous but bounded so a stuck server doesn't hang kart.new forever.
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
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("devcontainer: fetch %s: %s", url, resp.Status)
	}
	// 1 MiB is well past a reasonable devcontainer.json; anything larger is
	// almost certainly a misconfigured URL pointing at the wrong resource.
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("devcontainer: read %s: %w", url, err)
	}
	return body, nil
}

// NormalizeDevcontainer turns raw — a file path, a JSON string, or a URL —
// into a path on the local filesystem that devpod's
// --extra-devcontainer-path can consume. plans/PLAN.md § Flag composition
// step 5.
//
//   - file path (absolute or starting with ./ or ../, or an existing file):
//     returned as-is; the empty path cleanup does nothing.
//   - JSON (starts with `{` after trimming): written to tmpDir/devcontainer.json.
//   - URL (http:// or https://): downloaded to tmpDir/devcontainer.json.
//
// The returned cleanup func removes any temp file that this call created. It
// is safe to call even when no temp file was written. Tests can swap fetch
// with a canned responder.
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
		// JSON literal. Validate it parses before writing — catching the
		// typo here is much cheaper than having devpod complain mid-build.
		var probe any
		if err := json.Unmarshal([]byte(raw), &probe); err != nil {
			return "", cleanup, rpcerr.UserError(rpcerr.TypeInvalidFlag,
				"kart.new: --devcontainer is not valid JSON: %v", err)
		}
		return writeDevcontainerFile(tmpDir, []byte(raw))

	default:
		// Treat as a file path. Don't require existence here — the caller
		// may be on a system where the file exists but is unreadable by
		// the process; devpod will surface a clearer error on its own.
		// But an empty path is nonsense.
		if raw == "" {
			return "", cleanup, nil
		}
		return raw, cleanup, nil
	}
}

// writeDevcontainerFile writes body to tmpDir/devcontainer.json and returns
// a cleanup that removes the file. The tmpDir itself is the caller's to
// manage — kart.new keeps a single scratch dir per invocation so multiple
// temp files can share an rm -rf.
func writeDevcontainerFile(tmpDir string, body []byte) (string, func(), error) {
	cleanup := func() {}
	if tmpDir == "" {
		return "", cleanup, fmt.Errorf("devcontainer: tmpDir is required")
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return "", cleanup, fmt.Errorf("devcontainer: mkdir %s: %w", tmpDir, err)
	}
	path := filepath.Join(tmpDir, "devcontainer.json")
	if err := os.WriteFile(path, body, 0o600); err != nil {
		return "", cleanup, fmt.Errorf("devcontainer: write %s: %w", path, err)
	}
	cleanup = func() { _ = os.Remove(path) }
	return path, cleanup, nil
}
