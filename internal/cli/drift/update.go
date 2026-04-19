package drift

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/kurisu-agent/drift/internal/version"
)

type updateCmd struct {
	Check   bool   `help:"Check for an update without downloading."`
	Repo    string `name:"repo" hidden:"" default:"kurisu-agent/drift"`
	APIBase string `name:"api-base" hidden:"" default:"https://api.github.com"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	HTMLURL string    `json:"html_url"`
	Assets  []ghAsset `json:"assets"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

func runUpdate(ctx context.Context, ioStreams IO, cmd updateCmd) int {
	cur := version.Get().Version
	latest, err := fetchLatestRelease(ctx, cmd.APIBase, cmd.Repo)
	if err != nil {
		return emitError(ioStreams, fmt.Errorf("check failed: %w", err))
	}
	latestClean := strings.TrimPrefix(latest.TagName, "v")
	curClean := strings.TrimPrefix(cur, "v")
	fmt.Fprintf(ioStreams.Stdout, "current: %s\nlatest:  %s\n", orDevel(curClean), latestClean)

	if curClean == latestClean && cur != "devel" && cur != "" {
		fmt.Fprintln(ioStreams.Stdout, "up to date")
		return 0
	}
	if cmd.Check {
		fmt.Fprintf(ioStreams.Stdout, "update available: %s\n", latest.HTMLURL)
		return 0
	}
	if cur == "devel" || cur == "" {
		return emitError(ioStreams, errors.New("refusing to self-update a development build; rebuild from source or install a tagged release"))
	}

	asset, err := pickAsset(latest.Assets, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return emitError(ioStreams, err)
	}
	exe, err := os.Executable()
	if err != nil {
		return emitError(ioStreams, err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return emitError(ioStreams, err)
	}
	if err := downloadAndReplace(ctx, asset.BrowserDownloadURL, exe); err != nil {
		return emitError(ioStreams, err)
	}
	fmt.Fprintf(ioStreams.Stdout, "updated to %s\n", latestClean)
	return 0
}

func orDevel(s string) string {
	if s == "" {
		return "devel"
	}
	return s
}

func fetchLatestRelease(ctx context.Context, apiBase, repo string) (*ghRelease, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/latest", strings.TrimRight(apiBase, "/"), repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("github api returned %s", resp.Status)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, fmt.Errorf("decode release: %w", err)
	}
	if rel.TagName == "" {
		return nil, errors.New("github api returned a release with no tag_name")
	}
	return &rel, nil
}

// pickAsset matches the trailing _<os>_<arch>.tar.gz suffix rather than
// the version, so the logic survives version bumps.
func pickAsset(assets []ghAsset, goos, goarch string) (*ghAsset, error) {
	suffix := fmt.Sprintf("_%s_%s.tar.gz", goos, goarch)
	for i := range assets {
		if strings.HasPrefix(assets[i].Name, "drift_") && strings.HasSuffix(assets[i].Name, suffix) {
			return &assets[i], nil
		}
	}
	return nil, fmt.Errorf("no release asset for %s/%s", goos, goarch)
}

// downloadAndReplace uses rename(2) over the running executable — safe
// on Linux (incl. Android): the kernel keeps the old inode live for the
// current process.
func downloadAndReplace(ctx context.Context, url, dst string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s: %s", url, resp.Status)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return errors.New("tarball did not contain a drift binary")
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "drift" {
			continue
		}
		return writeAtomic(dst, tr)
	}
}

func writeAtomic(dst string, src io.Reader) error {
	tmp, err := os.CreateTemp(filepath.Dir(dst), ".drift-update-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	cleanup := func() { _ = os.Remove(tmpName) }
	if _, err := io.Copy(tmp, src); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Chmod(0o755); err != nil {
		_ = tmp.Close()
		cleanup()
		return err
	}
	if err := tmp.Close(); err != nil {
		cleanup()
		return err
	}
	if err := os.Rename(tmpName, dst); err != nil {
		cleanup()
		return err
	}
	return nil
}
