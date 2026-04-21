package drift

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
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
	if err := downloadAndReplace(context.Background(), srv.URL, dst, io.Discard); err != nil {
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
	err := downloadAndReplace(context.Background(), srv.URL, dst, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "did not contain") {
		t.Fatalf("expected missing-binary error, got: %v", err)
	}
}

func TestIsAndroidLinker(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/apex/com.android.runtime/bin/linker64", true},
		{"/apex/com.android.runtime/bin/linker", true},
		{"/system/bin/linker64", true},
		{"/system/bin/linker", true},
		{"/data/data/com.termux/files/usr/bin/drift", false},
		{"/usr/local/bin/drift", false},
		{"/apex/com.android.runtime/bin/drift", false},
		{"/system/bin/linker-prefix", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := isAndroidLinker(tc.path); got != tc.want {
			t.Errorf("isAndroidLinker(%q) = %v, want %v", tc.path, got, tc.want)
		}
	}
}

func TestResolveViaArgv0(t *testing.T) {
	const target = "/data/data/com.termux/files/usr/bin/drift"
	lookPath := func(name string) (string, error) {
		if name == "drift" {
			return target, nil
		}
		if filepath.IsAbs(name) {
			return name, nil
		}
		return "", os.ErrNotExist
	}
	got, err := resolveViaArgv0([]string{"drift", "update"}, lookPath)
	if err != nil {
		t.Fatalf("resolveViaArgv0: %v", err)
	}
	// EvalSymlinks will fail on this synthetic path; the function must
	// still return the unresolved lookPath result rather than error.
	if got != target {
		t.Errorf("got %q, want %q", got, target)
	}

	if _, err := resolveViaArgv0(nil, lookPath); err == nil {
		t.Error("expected error for empty argv")
	}
	if _, err := resolveViaArgv0([]string{""}, lookPath); err == nil {
		t.Error("expected error for empty argv[0]")
	}

	failLookPath := func(string) (string, error) { return "", os.ErrNotExist }
	if _, err := resolveViaArgv0([]string{"drift"}, failLookPath); err == nil {
		t.Error("expected error when lookPath fails")
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
