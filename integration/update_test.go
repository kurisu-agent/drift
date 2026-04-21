//go:build integration

package integration

import (
	"context"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// TestDriftUpdate_MissingResolvConf reproduces the Termux DNS failure
// without an Android device by running drift inside a container with
// /etc/resolv.conf bind-mounted to an empty file. In that state Go's
// pure-Go resolver picks its IANA-default loopback target ([::1]:53 /
// 127.0.0.1:53) with nothing listening, which is exactly what happens
// on Termux. A regression of the eager-fallback logic in dnsfix.go will
// surface here as the original ECONNREFUSED — the unit tests alone
// can't catch it because they don't exercise the full resolver path.
//
// This is intentionally a bare `docker run`: no sshd, no lakitu, no
// circuit harness. `drift update --check` is a pure client-side
// HTTP-to-GitHub call, so lighting up the whole circuit just to prove
// DNS resolution works would be wasteful. Real external network
// required (api.github.com) — acceptable given integration tests
// already pull base images.
func TestDriftUpdate_MissingResolvConf(t *testing.T) {
	t.Parallel()
	if testing.Short() {
		t.Skip("integration test requires docker")
	}
	if _, err := osexec.LookPath("docker"); err != nil {
		t.Skipf("docker not in PATH: %v", err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()

	bin := buildStaticDrift(t)

	// Empty file bound over /etc/resolv.conf: the docker daemon would
	// otherwise inject its own nameserver — we need the Termux symptom
	// (no nameserver → Go picks loopback with nothing home).
	emptyResolv := filepath.Join(t.TempDir(), "empty-resolv.conf")
	if err := os.WriteFile(emptyResolv, nil, 0o644); err != nil {
		t.Fatalf("write empty resolv.conf: %v", err)
	}

	// Minimal TLS trust: bind the host's CA bundle and point Go's TLS
	// loader at it. Alpine ships without ca-certificates by default,
	// and the rule "only this test" applies — no apk-add detour.
	hostCA := hostCABundle(t)

	args := []string{
		"run", "--rm",
		"-v", bin + ":/drift:ro",
		"-v", emptyResolv + ":/etc/resolv.conf:ro",
		"-v", hostCA + ":/certs.crt:ro",
		"-e", "SSL_CERT_FILE=/certs.crt",
		"alpine:3.20",
		"/drift", "--no-debug", "update", "--check",
	}
	cmd := osexec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("drift update --check with empty /etc/resolv.conf: %v\nargs: %v\noutput:\n%s",
			err, args, out)
	}
	// `latest:` appears in the success output regardless of whether an
	// update is available or not — what matters for this test is that
	// the GitHub API responded, which requires DNS to have resolved.
	if !strings.Contains(string(out), "latest:") {
		t.Fatalf("output missing 'latest:' — DNS fallback did not engage:\n%s", out)
	}
}

// buildStaticDrift compiles a CGO-free drift binary for linux/<hostarch>
// so it runs on alpine (musl) without a glibc dependency. Kept out of
// driftBinary() in harness.go because other tests exec drift directly
// on the host and would suffer a needless rebuild penalty.
func buildStaticDrift(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "drift")
	build := osexec.Command("go", "build", "-o", bin, "./cmd/drift")
	build.Dir = repoRoot(t)
	build.Env = append(os.Environ(),
		"CGO_ENABLED=0",
		"GOOS=linux",
		"GOARCH="+runtime.GOARCH,
	)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build static drift: %v\n%s", err, out)
	}
	return bin
}

// hostCABundle returns the first existing Linux CA bundle path. Kept
// narrow to the distros the test environments actually use (Debian-ish
// devcontainer, Ubuntu-ish CI runner); hitting RHEL/Fedora layouts
// would be overdesign.
func hostCABundle(t *testing.T) string {
	t.Helper()
	candidates := []string{
		"/etc/ssl/certs/ca-certificates.crt", // debian, ubuntu, alpine-with-apk
		"/etc/pki/tls/certs/ca-bundle.crt",   // rhel-family (just in case)
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	t.Skipf("no host CA bundle found in %v", candidates)
	return ""
}
