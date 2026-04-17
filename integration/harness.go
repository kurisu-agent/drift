//go:build integration

// Package integration hosts the tier-2 test harness that drives drift
// against a real sshd + lakitu running inside a container. Tests are
// build-tag-gated so `go test ./...` stays fast; `make integration` is
// the canonical entry point.
//
// Design overview (plans/PLAN.md § "Integration harness"):
//
//   - One container per test, built on demand from Dockerfile.circuit with
//     a freshly compiled lakitu copied in at build time. Short-lived so a
//     crashy test can't bleed state into the next.
//   - Per-test ephemeral SSH key pair; no secret material lives outside
//     the test's WorkDir.
//   - Per-test ~/.ssh/config generated into WorkDir and pointed at via the
//     DRIFT_CONFIG_DIR-style env overrides rather than clobbering the
//     developer's real ~/.ssh/config.
//   - `drift` is invoked as a subprocess (built from cmd/drift) rather than
//     in-process so the SSH transport path is exercised end-to-end.
package integration

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Circuit represents a running containerised sshd + lakitu. Call
// [StartCircuit] from a test, defer the returned Stop (or rely on
// t.Cleanup), then Exec commands against it through the drift binary.
type Circuit struct {
	t *testing.T

	ContainerID string
	// SSHPort is the host-side port forwarded to the container's 22.
	SSHPort int
	// User is the non-root user sshd drops to on login (matches
	// Dockerfile.circuit ARG CIRCUIT_USER).
	User string
	// WorkDir is the test's scratch tempdir: private key, ~/.ssh/config,
	// ~/.config/drift, ~/.drift — all rooted here.
	WorkDir string

	// Private paths into WorkDir.
	keyPath        string
	sshConfigPath  string
	driftConfigDir string
	driftHome      string
}

// StartCircuit builds the circuit image (idempotently, based on file
// timestamps), starts a fresh container, and returns a fully configured
// Circuit. Every step is fatal — a partially configured harness isn't
// useful to a test.
//
// The caller is responsible for registering the circuit with drift:
// `c.Drift(ctx, "circuit", "add", "test", "--host", c.Target(), "--no-probe")`
// or whatever the test needs.
func StartCircuit(ctx context.Context, t *testing.T) *Circuit {
	t.Helper()
	if testing.Short() {
		t.Skip("integration harness unavailable in -short mode")
	}

	c := &Circuit{
		t:       t,
		User:    "circuit",
		WorkDir: t.TempDir(),
	}
	c.keyPath = filepath.Join(c.WorkDir, "id_ed25519")
	c.sshConfigPath = filepath.Join(c.WorkDir, "ssh_config")
	c.driftConfigDir = filepath.Join(c.WorkDir, "drift_config")
	c.driftHome = filepath.Join(c.WorkDir, "home")
	for _, d := range []string{c.driftConfigDir, c.driftHome} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	c.generateKey()
	c.buildImage(ctx)
	c.runContainer(ctx)
	c.waitForSSH(ctx)
	c.writeSSHConfig()

	t.Cleanup(func() { c.Stop(context.Background()) })
	return c
}

// Drift runs `drift <args...>` with the per-test env (HOME, XDG_CONFIG_HOME,
// and a wrapper SSH_CONFIG override) pointed at WorkDir. Returns stdout and
// stderr separately and the exit code; never fatals — callers assert.
func (c *Circuit) Drift(ctx context.Context, args ...string) (stdout, stderr string, exitCode int) {
	c.t.Helper()
	bin := driftBinary(c.t)
	cmd := osexec.CommandContext(ctx, bin, args...)
	cmd.Env = append(os.Environ(),
		"HOME="+c.driftHome,
		"XDG_CONFIG_HOME="+filepath.Join(c.driftHome, ".config"),
		// The sshconf package writes its own block under
		// $XDG_CONFIG_HOME/drift/ssh_config; the include line it adds to
		// ~/.ssh/config is harmless since HOME points at our scratch dir.
	)
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err := cmd.Run()
	stdout = out.String()
	stderr = errBuf.String()
	if err == nil {
		return stdout, stderr, 0
	}
	var ee *osexec.ExitError
	if asExitError(err, &ee) {
		return stdout, stderr, ee.ExitCode()
	}
	c.t.Fatalf("drift %v: %v (stderr=%q)", args, err, stderr)
	return "", "", 1
}

// Target returns the `user@host:port` string tests pass to
// `drift circuit add --host`.
func (c *Circuit) Target() string {
	return fmt.Sprintf("%s@127.0.0.1:%d", c.User, c.SSHPort)
}

// SSHCommand runs a command as the circuit user inside the container via
// docker exec. This is the harness's way to do one-time setup (e.g.
// `lakitu init`) without routing through drift's RPC layer.
func SSHCommand(ctx context.Context, c *Circuit, name string, args ...string) error {
	argv := append([]string{"exec", "-u", c.User, c.ContainerID, name}, args...)
	return run(ctx, "docker", argv...)
}

// Stop tears down the container. Harmless to call twice.
func (c *Circuit) Stop(ctx context.Context) {
	if c == nil || c.ContainerID == "" {
		return
	}
	id := c.ContainerID
	c.ContainerID = ""
	_ = run(ctx, "docker", "rm", "-f", id)
}

// generateKey shells out to ssh-keygen to write an ephemeral ed25519
// keypair under WorkDir. Using the system tool avoids pulling in
// golang.org/x/crypto just for test scaffolding — the integration harness
// assumes sshd + ssh-keygen are already present in the devcontainer
// (openssh-client is part of the standard image).
func (c *Circuit) generateKey() {
	c.t.Helper()
	cmd := osexec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "drift integration test", "-f", c.keyPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		c.t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
}

// buildImage builds Dockerfile.circuit with the current repo's lakitu binary
// copied in. Go's build output goes into the test's WorkDir so parallel
// runs don't step on each other.
func (c *Circuit) buildImage(ctx context.Context) {
	c.t.Helper()
	lakituBin := filepath.Join(c.WorkDir, "lakitu")
	build := osexec.CommandContext(ctx, "go", "build", "-o", lakituBin, "./cmd/lakitu")
	build.Dir = repoRoot(c.t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if runtime.GOOS != "linux" {
		// Integration tests run inside the devcontainer (linux), but if a
		// contributor runs them from a mac host we still build linux/amd64
		// so the image is usable.
		build.Env = append(build.Env, "GOARCH=amd64")
	}
	if out, err := build.CombinedOutput(); err != nil {
		c.t.Fatalf("go build lakitu: %v\n%s", err, out)
	}
	ctxDir := filepath.Join(c.WorkDir, "docker-ctx")
	if err := os.MkdirAll(ctxDir, 0o755); err != nil {
		c.t.Fatalf("mkdir docker ctx: %v", err)
	}
	if err := copyFile(lakituBin, filepath.Join(ctxDir, "lakitu")); err != nil {
		c.t.Fatalf("stage lakitu: %v", err)
	}
	if err := copyFile(filepath.Join(repoRoot(c.t), "integration", "Dockerfile.circuit"), filepath.Join(ctxDir, "Dockerfile")); err != nil {
		c.t.Fatalf("stage dockerfile: %v", err)
	}
	if out, err := osexec.CommandContext(ctx, "docker", "build",
		"-t", "drift-integration-circuit",
		"--build-arg", "LAKITU_PATH=./lakitu",
		ctxDir,
	).CombinedOutput(); err != nil {
		c.t.Fatalf("docker build: %v\n%s", err, out)
	}
}

// runContainer starts a container with the test's authorized_keys injected
// and picks an ephemeral host port. Returns when the container is up; SSH
// readiness is handled by waitForSSH.
func (c *Circuit) runContainer(ctx context.Context) {
	c.t.Helper()
	port, err := freePort()
	if err != nil {
		c.t.Fatalf("free port: %v", err)
	}
	c.SSHPort = port
	out, err := osexec.CommandContext(ctx, "docker", "run", "-d", "--rm",
		"-p", fmt.Sprintf("%d:22", port),
		"drift-integration-circuit",
	).Output()
	if err != nil {
		c.t.Fatalf("docker run: %v", err)
	}
	c.ContainerID = strings.TrimSpace(string(out))

	// Push the authorized_keys file into the running container. Building
	// it into the image would bake the key into the layer cache and leak
	// across tests.
	authKeys, err := os.ReadFile(c.keyPath + ".pub")
	if err != nil {
		c.t.Fatalf("read pubkey: %v", err)
	}
	authPath := filepath.Join(c.WorkDir, "authorized_keys")
	if err := os.WriteFile(authPath, authKeys, 0o600); err != nil {
		c.t.Fatalf("stage authorized_keys: %v", err)
	}
	if err := run(ctx, "docker", "exec", c.ContainerID, "mkdir", "-p", "/home/"+c.User+"/.ssh"); err != nil {
		c.t.Fatalf("mkdir .ssh: %v", err)
	}
	if err := run(ctx, "docker", "cp", authPath, c.ContainerID+":/home/"+c.User+"/.ssh/authorized_keys"); err != nil {
		c.t.Fatalf("docker cp authorized_keys: %v", err)
	}
	if err := run(ctx, "docker", "exec", c.ContainerID, "chown", "-R", c.User+":"+c.User, "/home/"+c.User+"/.ssh"); err != nil {
		c.t.Fatalf("chown .ssh: %v", err)
	}
	if err := run(ctx, "docker", "exec", c.ContainerID, "chmod", "0600", "/home/"+c.User+"/.ssh/authorized_keys"); err != nil {
		c.t.Fatalf("chmod authorized_keys: %v", err)
	}
}

func (c *Circuit) waitForSSH(ctx context.Context) {
	c.t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", c.SSHPort), time.Second)
		if err == nil {
			conn.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	c.t.Fatalf("sshd never became reachable on 127.0.0.1:%d", c.SSHPort)
}

// writeSSHConfig emits a minimal ~/.ssh/config under WorkDir pointing at
// the forwarded sshd with StrictHostKeyChecking off — the host key changes
// every test run, so validating it would need a bespoke known_hosts rotation
// we don't need here.
func (c *Circuit) writeSSHConfig() {
	c.t.Helper()
	dir := filepath.Join(c.driftHome, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		c.t.Fatalf("mkdir .ssh: %v", err)
	}
	body := fmt.Sprintf(`Host %s-target
  HostName 127.0.0.1
  Port %d
  User %s
  IdentityFile %s
  IdentitiesOnly yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
`, c.t.Name(), c.SSHPort, c.User, c.keyPath)
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body), 0o600); err != nil {
		c.t.Fatalf("write ssh config: %v", err)
	}
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o755)
}

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

func repoRoot(t *testing.T) string {
	t.Helper()
	out, err := osexec.Command("git", "rev-parse", "--show-toplevel").Output()
	if err != nil {
		t.Fatalf("find repo root: %v", err)
	}
	return strings.TrimSpace(string(out))
}

func driftBinary(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "drift")
	build := osexec.Command("go", "build", "-o", bin, "./cmd/drift")
	build.Dir = repoRoot(t)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build drift: %v\n%s", err, out)
	}
	return bin
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := osexec.CommandContext(ctx, name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %v: %w\n%s", name, args, err, out)
	}
	return nil
}

// asExitError is a small shim so tests don't import errors just to unwrap
// ExitError. Mirrors errors.As semantics.
func asExitError(err error, target **osexec.ExitError) bool {
	for err != nil {
		if ee, ok := err.(*osexec.ExitError); ok {
			*target = ee
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
