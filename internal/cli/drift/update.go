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
	"time"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/version"
)

// maxUpdateTarEntry caps the in-memory buffer for a single tar entry
// during self-update. drift binaries are ~10 MB; 200 MiB is a generous
// ceiling that still refuses obviously-hostile payloads.
const maxUpdateTarEntry = 200 << 20

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
	fmt.Fprintln(ioStreams.Stderr, "→ checking latest release")
	latest, err := fetchLatestRelease(ctx, cmd.APIBase, cmd.Repo)
	if err != nil {
		return errfmt.Emit(ioStreams.Stderr, fmt.Errorf("check failed: %w", err))
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
		return errfmt.Emit(ioStreams.Stderr, errors.New("refusing to self-update a development build; rebuild from source or install a tagged release"))
	}

	asset, err := pickAsset(latest.Assets, runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return errfmt.Emit(ioStreams.Stderr, err)
	}
	fmt.Fprintf(ioStreams.Stderr, "→ selected asset %s (%s)\n", asset.Name, humanBytes(asset.Size))
	exe, err := os.Executable()
	if err != nil {
		return errfmt.Emit(ioStreams.Stderr, err)
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return errfmt.Emit(ioStreams.Stderr, err)
	}
	fmt.Fprintf(ioStreams.Stderr, "→ downloading %s\n", asset.BrowserDownloadURL)
	if err := downloadAndReplace(ctx, asset.BrowserDownloadURL, exe, ioStreams.Stderr); err != nil {
		return errfmt.Emit(ioStreams.Stderr, err)
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
// current process. progress writes a periodic `\r downloading X / Y`
// line so a stalled download is visibly localized rather than looking
// like a silent hang.
func downloadAndReplace(ctx context.Context, url, dst string, progress io.Writer) error {
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
	body := io.Reader(resp.Body)
	if progress != nil {
		body = newProgressReader(resp.Body, resp.ContentLength, progress)
	}
	gz, err := gzip.NewReader(body)
	if err != nil {
		return fmt.Errorf("gzip: %w", err)
	}
	defer func() { _ = gz.Close() }()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return errors.New("tarball did not contain a drift binary")
		}
		if err != nil {
			return fmt.Errorf("tar: %w", err)
		}
		if hdr.Typeflag != tar.TypeReg || filepath.Base(hdr.Name) != "drift" {
			continue
		}
		fmt.Fprintf(progress, "→ extracting %s (%s)\n", hdr.Name, humanBytes(hdr.Size))
		data, err := io.ReadAll(io.LimitReader(tr, maxUpdateTarEntry+1))
		if err != nil {
			return fmt.Errorf("tar: read drift entry: %w", err)
		}
		if int64(len(data)) > maxUpdateTarEntry {
			return fmt.Errorf("tar: drift entry exceeds %d byte limit", maxUpdateTarEntry)
		}
		fmt.Fprintf(progress, "→ writing %s\n", dst)
		return config.WriteFileAtomic(dst, data, 0o755)
	}
}

// progressReader wraps r and writes a one-line `\r downloading X / Y` to
// out at most every redrawInterval. Total <= 0 means Content-Length was
// missing; the line then drops the `/ Y` half. A final newline is
// emitted on EOF so the next stderr line lands cleanly.
type progressReader struct {
	r       io.Reader
	total   int64
	read    int64
	out     io.Writer
	last    time.Time
	started time.Time
}

const redrawInterval = 250 * time.Millisecond

func newProgressReader(r io.Reader, total int64, out io.Writer) *progressReader {
	return &progressReader{r: r, total: total, out: out, started: time.Now()}
}

func (pr *progressReader) Read(p []byte) (int, error) {
	n, err := pr.r.Read(p)
	pr.read += int64(n)
	now := time.Now()
	atEOF := errors.Is(err, io.EOF)
	if atEOF || now.Sub(pr.last) >= redrawInterval {
		pr.last = now
		pr.draw(atEOF)
	}
	return n, err
}

func (pr *progressReader) draw(final bool) {
	elapsed := time.Since(pr.started)
	rate := ""
	if elapsed > 0 {
		rate = fmt.Sprintf(" @ %s/s", humanBytes(int64(float64(pr.read)/elapsed.Seconds())))
	}
	if pr.total > 0 {
		pct := float64(pr.read) / float64(pr.total) * 100
		fmt.Fprintf(pr.out, "\r  %s / %s (%5.1f%%)%s", humanBytes(pr.read), humanBytes(pr.total), pct, rate)
	} else {
		fmt.Fprintf(pr.out, "\r  %s%s", humanBytes(pr.read), rate)
	}
	if final {
		fmt.Fprintln(pr.out)
	}
}

// humanBytes renders sizes in IEC units (KiB/MiB/GiB) since CDN downloads
// of single binaries land squarely in MiB territory. Decimal SI would
// just make the math feel slightly wrong.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n2 := n / unit; n2 >= unit; n2 /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
