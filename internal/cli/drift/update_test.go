package drift

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestPickAsset(t *testing.T) {
	assets := []ghAsset{
		{Name: "drift_1.2.3_linux_amd64.tar.gz"},
		{Name: "drift_1.2.3_linux_arm64.tar.gz"},
		{Name: "drift_1.2.3_android_arm64.tar.gz"},
		{Name: "lakitu_1.2.3_linux_amd64.tar.gz"},
		{Name: "checksums.txt"},
	}
	got, err := pickAsset(assets, "android", "arm64")
	if err != nil {
		t.Fatalf("pickAsset: %v", err)
	}
	if got.Name != "drift_1.2.3_android_arm64.tar.gz" {
		t.Errorf("name = %q", got.Name)
	}
	if _, err := pickAsset(assets, "windows", "386"); err == nil {
		t.Error("expected error for unknown platform")
	}
}

func TestFetchLatestRelease(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/x/y/releases/latest" {
			http.NotFound(w, r)
			return
		}
		_ = json.NewEncoder(w).Encode(ghRelease{
			TagName: "v1.2.3",
			HTMLURL: "https://example/release",
			Assets:  []ghAsset{{Name: "drift_1.2.3_linux_amd64.tar.gz"}},
		})
	}))
	defer srv.Close()
	rel, err := fetchLatestRelease(context.Background(), srv.URL, "x/y")
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if rel.TagName != "v1.2.3" {
		t.Errorf("tag = %q", rel.TagName)
	}
}

func TestFetchLatestRelease_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.NotFound(w, r)
	}))
	defer srv.Close()
	if _, err := fetchLatestRelease(context.Background(), srv.URL, "x/y"); err == nil {
		t.Fatal("expected error on 404")
	}
}

func TestRunUpdate_CheckPrintsUpdateURL(t *testing.T) {
	// Under `go test` version.Get() returns "devel", so --check always
	// reports an update available — that's the happy-path parse we assert.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ghRelease{
			TagName: "v9.9.9",
			HTMLURL: "https://example/release",
		})
	}))
	defer srv.Close()

	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}
	code := runUpdate(context.Background(), io, updateCmd{
		Check:   true,
		Repo:    "x/y",
		APIBase: srv.URL,
	})
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %q", code, stderr.String())
	}
	out := stdout.String()
	if !strings.Contains(out, "latest:") || !strings.Contains(out, "9.9.9") {
		t.Errorf("stdout missing latest version:\n%s", out)
	}
	if !strings.Contains(out, "update available") {
		t.Errorf("stdout missing update-available line:\n%s", out)
	}
}

func TestRunUpdate_RefusesDevelOnInstall(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(ghRelease{TagName: "v1.2.3"})
	}))
	defer srv.Close()
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}
	code := runUpdate(context.Background(), io, updateCmd{
		Repo:    "x/y",
		APIBase: srv.URL,
	})
	if code == 0 {
		t.Fatalf("expected non-zero exit for devel self-update")
	}
	if !strings.Contains(stderr.String(), "development build") {
		t.Errorf("stderr missing refusal reason: %q", stderr.String())
	}
}

// TestDownloadAndReplace exercises the full swap path: it serves a gzipped
// tarball whose "drift" entry contains sentinel bytes, points downloadAndReplace
// at a throwaway file, and asserts the file now contains those bytes.
func TestDownloadAndReplace(t *testing.T) {
	payload := []byte("NEW_DRIFT_BINARY_" + runtime.GOOS + "_" + runtime.GOARCH)
	tarball := buildTarball(t, "drift", payload)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(tarball)
	}))
	defer srv.Close()

	dir := t.TempDir()
	dst := filepath.Join(dir, "drift")
	if err := os.WriteFile(dst, []byte("old"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := downloadAndReplace(context.Background(), srv.URL, dst); err != nil {
		t.Fatalf("downloadAndReplace: %v", err)
	}
	got, err := os.ReadFile(dst)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, payload) {
		t.Errorf("dst contents mismatch:\ngot:  %q\nwant: %q", got, payload)
	}
	info, err := os.Stat(dst)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&0o111 == 0 {
		t.Errorf("dst not executable: mode=%v", info.Mode())
	}
}

func TestDownloadAndReplace_NoDriftEntry(t *testing.T) {
	tarball := buildTarball(t, "other-file", []byte("nope"))
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(tarball)
	}))
	defer srv.Close()
	dst := filepath.Join(t.TempDir(), "drift")
	_ = os.WriteFile(dst, []byte("old"), 0o755)
	err := downloadAndReplace(context.Background(), srv.URL, dst)
	if err == nil || !strings.Contains(err.Error(), "did not contain") {
		t.Fatalf("expected missing-binary error, got: %v", err)
	}
}

func buildTarball(t *testing.T, name string, body []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{
		Name:     name,
		Mode:     0o755,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}
