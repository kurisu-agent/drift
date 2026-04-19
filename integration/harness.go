//go:build integration

// Package integration hosts the tier-2 test harness that drives drift
// against a real sshd + lakitu running inside a container. Tests are
// build-tag-gated so `go test ./...` stays fast; `make integration` is
// the canonical entry point.
//
// Design overview:
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
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"os"
	osexec "os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
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
	shimDir        string

	// runID is a 6-byte hex id unique to this test run; it tags the
	// circuit container and any devpod workspaces spawned on the host so
	// orphaned state from a crashed run can be swept by label.
	runID string
	// containerName is the --name passed to docker run. Derived from runID
	// so the pre-test sweep can find stragglers by name pattern.
	containerName string
	// kartPrefix is prepended to every kart created during a test. Used
	// both to satisfy drift's name regex and to namespace devpod workspace
	// containers (which show up on the host as `devpod-<kartname>-…`).
	kartPrefix string
	// sharedScratch is a per-run path that exists at the SAME location on
	// both the devcontainer and the circuit (bind-mount). Used as the
	// circuit's TMPDIR so drift scratch dirs created there are also
	// resolvable by the outer dockerd when devpod bind-mounts the source.
	sharedScratch string

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

	runID := randomHex(6)
	c := &Circuit{
		t:             t,
		User:          "circuit",
		WorkDir:       t.TempDir(),
		runID:         runID,
		containerName: "drift-int-" + runID,
		kartPrefix:    "int-" + runID + "-",
	}
	c.keyPath = filepath.Join(c.WorkDir, "id_ed25519")
	c.sshConfigPath = filepath.Join(c.WorkDir, "ssh_config")
	c.driftConfigDir = filepath.Join(c.WorkDir, "drift_config")
	c.driftHome = filepath.Join(c.WorkDir, "home")
	c.shimDir = filepath.Join(c.WorkDir, "shims")
	for _, d := range []string{c.driftConfigDir, c.driftHome, c.shimDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", d, err)
		}
	}

	// Belt-and-braces pre-test sweep: any leftover devpod containers from
	// a crashed earlier run of this test binary get torn down before we
	// start. Running this before generateKey/buildImage keeps the startup
	// cost predictable even when the host was left dirty.
	sweepIntegrationContainers(ctx, t, "")

	c.generateKey()
	c.buildImage(ctx)
	c.runContainer(ctx)
	c.waitForSSH(ctx)
	c.writeSSHConfig()
	c.writeSSHShim()

	t.Cleanup(func() {
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		// Kill any devpod workspace containers we spawned before tearing
		// down the circuit — otherwise they'd be orphaned on the host
		// with no route back to ~/.devpod state.
		sweepIntegrationContainers(bg, t, c.kartPrefix)
		c.Stop(bg)
	})
	return c
}

// Drift runs `drift <args...>` with the per-test env (HOME, XDG_CONFIG_HOME,
// and a wrapper SSH_CONFIG override) pointed at WorkDir. Returns stdout and
// stderr separately and the exit code; never fatals — callers assert.
func (c *Circuit) Drift(ctx context.Context, args ...string) (stdout, stderr string, exitCode int) {
	c.t.Helper()
	bin := driftBinary(c.t)
	cmd := osexec.CommandContext(ctx, bin, args...)
	cmd.Env = overlayEnv(map[string]string{
		"HOME":            c.driftHome,
		"XDG_CONFIG_HOME": filepath.Join(c.driftHome, ".config"),
		// Prepend the harness shim dir so every `ssh` spawned by drift
		// (rpc/client.SSHTransport, ssh-proxy's nested ssh, warmup probe)
		// routes through the harness-managed ssh_config. See writeSSHShim
		// for why $HOME alone doesn't do the trick.
		"PATH": c.shimDir + string(os.PathListSeparator) + os.Getenv("PATH"),
	})
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
//
// The host's /var/run/docker.sock is bind-mounted so devpod's docker provider
// inside the circuit can reach the outer daemon. The host's docker group GID
// (read from the socket) is granted to the circuit user via --group-add so
// the mounted socket is writable without --privileged or chmod.
//
// A deterministic --name prefix (drift-int-<hex>) is assigned so stragglers
// from a crashed run can be swept by name even if the harness goroutine never
// got to run its t.Cleanup.
func (c *Circuit) runContainer(ctx context.Context) {
	c.t.Helper()
	port, err := freePort()
	if err != nil {
		c.t.Fatalf("free port: %v", err)
	}
	c.SSHPort = port

	dockerGID, err := dockerSocketGID()
	if err != nil {
		c.t.Fatalf("stat /var/run/docker.sock: %v", err)
	}

	// Share a per-run scratch dir at the SAME path on both the devcontainer
	// and the circuit so that when drift creates /tmp/drift-kart-<id>/ on
	// the circuit and hands the path to devpod, the outer dockerd (running
	// on the devcontainer, resolving bind-mount sources against its own
	// filesystem) can find the directory too. `TMPDIR` is set on the
	// circuit's main process and propagated to ssh login sessions via
	// PermitUserEnvironment + ~/.ssh/environment (written below).
	c.sharedScratch = "/tmp/drift-int-scratch-" + c.runID
	if err := os.MkdirAll(c.sharedScratch, 0o777); err != nil {
		c.t.Fatalf("mkdir shared scratch: %v", err)
	}
	if err := os.Chmod(c.sharedScratch, 0o777); err != nil {
		c.t.Fatalf("chmod shared scratch: %v", err)
	}
	c.t.Cleanup(func() { _ = os.RemoveAll(c.sharedScratch) })
	devpodHome := c.sharedScratch + "/.devpod-home"
	if err := os.MkdirAll(devpodHome, 0o777); err != nil {
		c.t.Fatalf("mkdir devpod home: %v", err)
	}

	args := []string{
		"run", "-d", "--rm",
		"--name", c.containerName,
		"--label", "drift.integration=1",
		"--label", "drift.integration.run=" + c.runID,
		"-p", fmt.Sprintf("%d:22", port),
		"-v", "/var/run/docker.sock:/var/run/docker.sock",
		"-v", c.sharedScratch + ":" + c.sharedScratch,
		"-e", "TMPDIR=" + c.sharedScratch,
		// DEVPOD_HOME is set as a docker run env var so it applies to BOTH
		// `docker exec` (used by SSHCommand for one-time setup like `lakitu
		// init`) AND sshd-spawned sessions (used by `drift` over real SSH).
		// Keeping a single source of truth prevents provider registrations
		// written under one devpod home from being invisible to the other.
		"-e", "DEVPOD_HOME=" + devpodHome,
		"--group-add", strconv.Itoa(dockerGID),
		"drift-integration-circuit",
	}
	out, err := osexec.CommandContext(ctx, "docker", args...).Output()
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

	// Pin TMPDIR + DEVPOD_HOME for ssh sessions via ~/.ssh/environment
	// (sshd's PermitUserEnvironment is enabled in the Dockerfile). lakitu's
	// kart.new uses os.MkdirTemp("", …) which honors TMPDIR, so scratch
	// dirs land in the shared bind mount path devpod can resolve.
	// DEVPOD_HOME redirects devpod's agent/contexts/... tree into the
	// shared mount too so --clone of a file:// URL produces a workspace
	// source the outer dockerd can bind-mount into the devcontainer.
	// The docker run -e above covers docker-exec entry points; this
	// ~/.ssh/environment entry covers sshd-spawned sessions, which don't
	// inherit PID 1's env.
	envLine := "TMPDIR=" + c.sharedScratch + "\n" +
		"DEVPOD_HOME=" + devpodHome + "\n"
	envPath := filepath.Join(c.WorkDir, "ssh_environment")
	if err := os.WriteFile(envPath, []byte(envLine), 0o600); err != nil {
		c.t.Fatalf("stage ssh environment: %v", err)
	}
	if err := run(ctx, "docker", "cp", envPath, c.ContainerID+":/home/"+c.User+"/.ssh/environment"); err != nil {
		c.t.Fatalf("docker cp ssh environment: %v", err)
	}
	if err := run(ctx, "docker", "exec", c.ContainerID, "chown", c.User+":"+c.User, "/home/"+c.User+"/.ssh/environment"); err != nil {
		c.t.Fatalf("chown ssh environment: %v", err)
	}
	if err := run(ctx, "docker", "exec", c.ContainerID, "chmod", "0600", "/home/"+c.User+"/.ssh/environment"); err != nil {
		c.t.Fatalf("chmod ssh environment: %v", err)
	}

	// sshd → initgroups() resets supplementary groups to /etc/group at
	// login, so the circuit user only inherits `--group-add`'s docker GID
	// inside sshd itself — not inside sessions. Add the user to a group
	// whose GID matches the bind-mounted socket's owner so subsequent
	// `docker ps` from a login shell can talk to the outer daemon.
	groupSetup := fmt.Sprintf(
		`getent group %[1]d >/dev/null || groupadd -g %[1]d -o docker-host && usermod -aG %[1]d %s`,
		dockerGID, c.User,
	)
	if err := run(ctx, "docker", "exec", c.ContainerID, "sh", "-c", groupSetup); err != nil {
		c.t.Fatalf("add circuit user to docker group: %v", err)
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
//
// The Host list includes `drift.*` so that any `drift.<circuit>` or
// `drift.<circuit>.<kart>` alias drift constructs resolves to the test
// container. This lets Drift() invocations exercise the real SSH transport
// without relying on drift's managed ssh_config writer (which tests bypass
// via --no-ssh-config so the harness owns identity/port concerns).
func (c *Circuit) writeSSHConfig() {
	c.t.Helper()
	dir := filepath.Join(c.driftHome, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		c.t.Fatalf("mkdir .ssh: %v", err)
	}
	// Three blocks:
	//   * Host drift.*.*       → ProxyCommand + identity/user. HostName is
	//     *not* set so %h in ProxyCommand expands to the original alias
	//     (ssh_config(5): %h becomes HostName when HostName is present).
	//   * Match host drift.*,!drift.*.* → HostName/Port for the circuit-only
	//     aliases; explicitly excludes drift.*.* to keep the ProxyCommand
	//     path's %h untouched.
	//   * Host <test>-target  → plain direct-connect alias, retained for
	//     any test that wants a raw SSH endpoint.
	body := fmt.Sprintf(`Host drift.*.*
  ProxyCommand drift ssh-proxy %%h %%p
  User %[3]s
  IdentityFile %[4]s
  IdentitiesOnly yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null

Match host "drift.*,!drift.*.*"
  HostName 127.0.0.1
  Port %[2]d
  User %[3]s
  IdentityFile %[4]s
  IdentitiesOnly yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null

Host %[1]s-target
  HostName 127.0.0.1
  Port %[2]d
  User %[3]s
  IdentityFile %[4]s
  IdentitiesOnly yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
`, c.t.Name(), c.SSHPort, c.User, c.keyPath)
	if err := os.WriteFile(filepath.Join(dir, "config"), []byte(body), 0o600); err != nil {
		c.t.Fatalf("write ssh config: %v", err)
	}
}

// LakituRPC shells a single JSON-RPC request into `lakitu rpc` running on
// the circuit via `docker exec -i`. It returns the decoded result bytes on
// success or a *rpcerr.Error on an RPC-level failure. Transport-level
// failures (docker exec exited non-zero) fail the test.
//
// Tests use this for method surfaces drift's CLI does not expose directly
// (chest.*, character.* — both are driven by the warmup wizard in
// production). It keeps integration coverage close to the RPC catalog
// without synthesizing an interactive wizard.
func (c *Circuit) LakituRPC(ctx context.Context, method string, params any) (json.RawMessage, error) {
	c.t.Helper()
	raw := json.RawMessage(`{}`)
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			c.t.Fatalf("LakituRPC: marshal params: %v", err)
		}
		raw = b
	}
	req := wire.Request{
		JSONRPC: wire.Version,
		Method:  method,
		Params:  raw,
		ID:      json.RawMessage(`1`),
	}
	reqBytes, err := json.Marshal(&req)
	if err != nil {
		c.t.Fatalf("LakituRPC: marshal request: %v", err)
	}
	cmd := osexec.CommandContext(ctx, "docker", "exec", "-i", "-u", c.User, c.ContainerID, "lakitu", "rpc")
	cmd.Stdin = bytes.NewReader(reqBytes)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		c.t.Fatalf("lakitu rpc transport: %v\nstderr=%s", err, stderr.String())
	}
	resp, err := wire.DecodeResponse(&stdout)
	if err != nil {
		c.t.Fatalf("lakitu rpc: decode response: %v\nraw=%s", err, stdout.String())
	}
	if resp.Error != nil {
		return nil, rpcerr.FromWire(resp.Error)
	}
	return resp.Result, nil
}

// KartName returns a valid per-run kart name with the test's namespace
// prefix, e.g. `int-ab12cd34ef56-<suffix>`. Using the prefix makes the
// post-test container sweep (see sweepIntegrationContainers) able to find
// every devpod workspace this test created.
func (c *Circuit) KartName(suffix string) string {
	return c.kartPrefix + suffix
}

// writeSSHShim installs an `ssh` wrapper under c.shimDir that forces
// `-F <harness-config>` on every invocation. OpenSSH locates ~/.ssh/config
// via getpwuid(getuid()) rather than $HOME, so setting HOME alone does not
// redirect config resolution for subprocesses (drift ssh-proxy's nested ssh,
// drift's rpc/client.SSHTransport, warmup probe, …). Injecting the shim on
// PATH is the portable workaround — tests that want the real ssh can
// invoke /usr/bin/ssh directly.
func (c *Circuit) writeSSHShim() {
	c.t.Helper()
	path := filepath.Join(c.shimDir, "ssh")
	realSSH, err := osexec.LookPath("ssh")
	if err != nil {
		c.t.Fatalf("lookup real ssh: %v", err)
	}
	body := fmt.Sprintf(`#!/bin/sh
exec %s -F %s "$@"
`, realSSH, filepath.Join(c.driftHome, ".ssh", "config"))
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		c.t.Fatalf("write ssh shim: %v", err)
	}
}

// StageStarter creates a bare git repo at /tmp/<name>.git inside the circuit
// containing one commit with the supplied files (relative-path → contents)
// and returns its file:// URL. Tests use this to feed drift --starter a
// deterministic, offline source without relying on the network.
func (c *Circuit) StageStarter(ctx context.Context, name string, files map[string]string) string {
	c.t.Helper()
	work := "/tmp/" + name + "-work"
	bare := "/tmp/" + name + ".git"
	var setup strings.Builder
	setup.WriteString("set -e\n")
	setup.WriteString("rm -rf " + work + " " + bare + "\n")
	setup.WriteString("mkdir -p " + work + "\n")
	setup.WriteString("cd " + work + "\n")
	setup.WriteString("git init -q -b main\n")
	setup.WriteString("git config user.email t@example.com\n")
	setup.WriteString("git config user.name T\n")
	for path, body := range files {
		setup.WriteString("mkdir -p " + filepath.ToSlash(filepath.Dir(path)) + "\n")
		// Use a heredoc so backtick/quote content survives unchanged.
		setup.WriteString("cat > " + path + " <<'__DRIFT_EOF__'\n")
		setup.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			setup.WriteString("\n")
		}
		setup.WriteString("__DRIFT_EOF__\n")
		setup.WriteString("git add " + path + "\n")
	}
	setup.WriteString("git commit -qm init\n")
	setup.WriteString("git clone -q --bare . " + bare + "\n")
	if err := SSHCommand(ctx, c, "sh", "-c", setup.String()); err != nil {
		c.t.Fatalf("stage starter %s: %v", name, err)
	}
	return "file://" + bare
}

// StageCloneFixture stages a bare git repo at /srv/gitrepos/<name>.git on
// the circuit and returns a file:// URL pointing at it.
//
// The flow assumes the circuit's DEVPOD_HOME is under sharedScratch (set in
// [Circuit.runContainer] via ~/.ssh/environment) so devpod v0.22's agent
// clone writes into a path the outer dockerd can also resolve when it
// bind-mounts the workspace source into the devcontainer.
//
// The bare repo is chowned to UID 1000 — the circuit user — so git on the
// circuit side doesn't trip its "dubious ownership" safety check when
// cloning.
func (c *Circuit) StageCloneFixture(ctx context.Context, name string, files map[string]string) string {
	c.t.Helper()

	work := "/srv/gitrepos/" + name + "-work"
	bare := "/srv/gitrepos/" + name + ".git"
	var setup strings.Builder
	setup.WriteString("set -e\n")
	setup.WriteString("mkdir -p /srv/gitrepos\n")
	setup.WriteString("rm -rf " + work + " " + bare + "\n")
	setup.WriteString("mkdir -p " + work + "\n")
	setup.WriteString("cd " + work + "\n")
	setup.WriteString("git init -q -b main\n")
	setup.WriteString("git config user.email t@example.com\n")
	setup.WriteString("git config user.name T\n")
	for path, body := range files {
		setup.WriteString("mkdir -p " + filepath.ToSlash(filepath.Dir(path)) + "\n")
		setup.WriteString("cat > " + path + " <<'__DRIFT_EOF__'\n")
		setup.WriteString(body)
		if !strings.HasSuffix(body, "\n") {
			setup.WriteString("\n")
		}
		setup.WriteString("__DRIFT_EOF__\n")
		setup.WriteString("git add " + path + "\n")
	}
	setup.WriteString("git commit -qm init\n")
	setup.WriteString("git clone -q --bare . " + bare + "\n")
	// Circuit user runs the clone; match the bare repo's owner so git's
	// dubious-ownership guard doesn't trip.
	setup.WriteString("chown -R 1000:1000 /srv/gitrepos\n")
	setup.WriteString("chmod -R a+rX /srv/gitrepos\n")
	// -u root: staging writes under /srv/gitrepos, which the circuit user
	// cannot create. The chown line hands the final tree back to UID 1000.
	if err := run(ctx, "docker", "exec", "-u", "root", c.ContainerID, "sh", "-c", setup.String()); err != nil {
		c.t.Fatalf("stage clone fixture %s: %v", name, err)
	}
	return "file://" + bare
}

// InstallDevpodShim overwrites /usr/local/bin/devpod on the circuit with a
// test shim. devpod requires a docker daemon that isn't present in the
// integration image; tests that need `devpod ssh --stdio` semantics inject a
// shim that fakes just enough of the subcommand surface (usually by piping
// stdio to local sshd). The shim runs as root so it can install atomically.
func (c *Circuit) InstallDevpodShim(ctx context.Context, body string) {
	c.t.Helper()
	shimPath := filepath.Join(c.WorkDir, "devpod-shim")
	if err := os.WriteFile(shimPath, []byte(body), 0o755); err != nil {
		c.t.Fatalf("write devpod shim: %v", err)
	}
	if err := run(ctx, "docker", "cp", shimPath, c.ContainerID+":/usr/local/bin/devpod"); err != nil {
		c.t.Fatalf("docker cp devpod shim: %v", err)
	}
	if err := run(ctx, "docker", "exec", c.ContainerID, "chmod", "0755", "/usr/local/bin/devpod"); err != nil {
		c.t.Fatalf("chmod devpod shim: %v", err)
	}
}

// InstallBin writes body to /usr/local/bin/<name> inside the circuit
// container (as root, via docker cp) and makes it executable. Tests use it
// to drop ad-hoc shims on the circuit PATH — `claude` for the drift-ai
// path, anything else a future test needs.
func (c *Circuit) InstallBin(ctx context.Context, name, body string) {
	c.t.Helper()
	host := filepath.Join(c.WorkDir, name+"-shim")
	if err := os.WriteFile(host, []byte(body), 0o755); err != nil {
		c.t.Fatalf("write %s shim: %v", name, err)
	}
	if err := run(ctx, "docker", "cp", host, c.ContainerID+":/usr/local/bin/"+name); err != nil {
		c.t.Fatalf("docker cp %s shim: %v", name, err)
	}
	if err := run(ctx, "docker", "exec", c.ContainerID, "chmod", "0755", "/usr/local/bin/"+name); err != nil {
		c.t.Fatalf("chmod %s shim: %v", name, err)
	}
}

// DevpodInvocation is one recorded `devpod …` call captured by the
// recorder shim. Argv is the raw argv drift handed to devpod; ArtifactDir
// is the path inside the circuit where the shim preserved any file or dir
// arguments (starter source, layer-1 dotfiles tree, extra-devcontainer
// file) so assertions can inspect what drift actually materialized.
type DevpodInvocation struct {
	Argv        []string `json:"argv"`
	ArtifactDir string   `json:"artifact_dir,omitempty"`
}

// DevpodRecorder is the test-side accessor for the invocation log.
// [InstallDevpodRecorder] installs a Go shim in place of /usr/local/bin/devpod
// that writes every argv as a JSON line; Invocations() docker-cats the log
// and decodes it. The recorder owns no background state — reads are
// side-effect-free and can be called multiple times.
type DevpodRecorder struct {
	c *Circuit
}

// InstallDevpodRecorder compiles the in-tree recorder shim and installs it as
// /usr/local/bin/devpod on the circuit. Returns a DevpodRecorder for reading
// the captured argv back. Subsequent calls to drift (kart.new, kart.start,
// kart.info, …) that reach lakitu will have every devpod invocation logged
// to /tmp/devpod-invocations.log inside the container.
//
// The shim returns exit 0 for all subcommands and emits canned JSON for
// `status` (Running) and `list` (empty) so drift's kart.info / kart.list
// paths keep working. Tests that need bespoke responses can further override
// with [InstallDevpodShim].
func (c *Circuit) InstallDevpodRecorder(ctx context.Context) *DevpodRecorder {
	c.t.Helper()
	binPath := filepath.Join(c.WorkDir, "devpod-shim")
	build := osexec.CommandContext(ctx, "go", "build", "-o", binPath, "./integration/shim/devpod")
	build.Dir = repoRoot(c.t)
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if out, err := build.CombinedOutput(); err != nil {
		c.t.Fatalf("build devpod shim: %v\n%s", err, out)
	}
	if err := run(ctx, "docker", "cp", binPath, c.ContainerID+":/usr/local/bin/devpod"); err != nil {
		c.t.Fatalf("docker cp devpod shim: %v", err)
	}
	if err := run(ctx, "docker", "exec", c.ContainerID, "chmod", "0755", "/usr/local/bin/devpod"); err != nil {
		c.t.Fatalf("chmod devpod shim: %v", err)
	}
	// Truncate any stale log from a previous recorder install in the same
	// container (unlikely — each test gets a fresh circuit — but cheap).
	_ = run(ctx, "docker", "exec", c.ContainerID, "rm", "-f", "/tmp/devpod-invocations.log")
	return &DevpodRecorder{c: c}
}

// Invocations reads the log and returns every recorded devpod call, in the
// order they arrived. Empty slice when no invocations have happened yet.
func (r *DevpodRecorder) Invocations(ctx context.Context) []DevpodInvocation {
	r.c.t.Helper()
	out, err := osexec.CommandContext(ctx, "docker", "exec", r.c.ContainerID,
		"cat", "/tmp/devpod-invocations.log").Output()
	if err != nil {
		// Missing log = no calls yet; docker exec emits non-zero for a
		// missing file, which we distinguish from a real failure by
		// checking the ExitError type.
		if _, ok := err.(*osexec.ExitError); ok {
			return nil
		}
		r.c.t.Fatalf("read devpod log: %v", err)
	}
	var invs []DevpodInvocation
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		var rec DevpodInvocation
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			r.c.t.Fatalf("decode devpod log line %q: %v", line, err)
		}
		invs = append(invs, rec)
	}
	return invs
}

// FindUp returns the first `devpod up …` invocation recorded, or nil if
// no up call has been logged. Tune/feature tests use this to assert on
// the composed argv.
func (r *DevpodRecorder) FindUp(ctx context.Context) *DevpodInvocation {
	for _, inv := range r.Invocations(ctx) {
		if len(inv.Argv) > 0 && inv.Argv[0] == "up" {
			cp := inv
			return &cp
		}
	}
	return nil
}

// FindInstallDotfiles returns the first `devpod agent workspace
// install-dotfiles …` invocation, or nil. Used to locate the artifact
// directory holding the generated layer-1 dotfiles tree.
func (r *DevpodRecorder) FindInstallDotfiles(ctx context.Context) *DevpodInvocation {
	for _, inv := range r.Invocations(ctx) {
		if len(inv.Argv) >= 3 && inv.Argv[0] == "agent" && inv.Argv[2] == "install-dotfiles" {
			cp := inv
			return &cp
		}
	}
	return nil
}

// execInContainer runs a command inside the circuit container as the
// circuit user and returns captured stdout. stderr is routed to the test
// log; non-zero exit fatals. Used by tests that need to inspect artifact
// contents via real commands like `git -C`, not just cat/ls.
func (c *Circuit) ExecInContainer(ctx context.Context, name string, args ...string) []byte {
	c.t.Helper()
	argv := append([]string{"exec", "-u", c.User, c.ContainerID, name}, args...)
	cmd := osexec.CommandContext(ctx, "docker", argv...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		c.t.Fatalf("docker exec %s %v: %v\nstderr=%s", name, args, err, stderr.String())
	}
	return stdout.Bytes()
}

// ReadArtifact docker-execs `cat` for a path inside the shim's artifact
// tree and returns its raw bytes. relPath is joined against inv.ArtifactDir.
// Missing files fatal the test.
func (c *Circuit) ReadArtifact(ctx context.Context, inv *DevpodInvocation, relPath string) []byte {
	c.t.Helper()
	if inv == nil || inv.ArtifactDir == "" {
		c.t.Fatalf("ReadArtifact: invocation has no artifact_dir")
	}
	full := filepath.ToSlash(filepath.Join(inv.ArtifactDir, relPath))
	out, err := osexec.CommandContext(ctx, "docker", "exec", c.ContainerID, "cat", full).Output()
	if err != nil {
		var stderr string
		if ee, ok := err.(*osexec.ExitError); ok {
			stderr = string(ee.Stderr)
		}
		c.t.Fatalf("ReadArtifact %s: %v\n%s", full, err, stderr)
	}
	return out
}

// ListArtifact docker-execs `ls -1` for a directory inside the shim's
// artifact tree and returns the file names. Missing directory yields nil.
func (c *Circuit) ListArtifact(ctx context.Context, inv *DevpodInvocation, relDir string) []string {
	c.t.Helper()
	if inv == nil || inv.ArtifactDir == "" {
		c.t.Fatalf("ListArtifact: invocation has no artifact_dir")
	}
	full := filepath.ToSlash(filepath.Join(inv.ArtifactDir, relDir))
	// -A: include hidden dotfile entries (e.g. .git in a stripped starter
	// tree) but not . and .. so the returned slice is just real names.
	out, err := osexec.CommandContext(ctx, "docker", "exec", c.ContainerID, "ls", "-1A", full).Output()
	if err != nil {
		if _, ok := err.(*osexec.ExitError); ok {
			return nil
		}
		c.t.Fatalf("ListArtifact %s: %v", full, err)
	}
	var names []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line != "" {
			names = append(names, line)
		}
	}
	return names
}

// DriftBinDir returns the directory that contains the test-built drift
// binary. Callers export it on PATH so ProxyCommand invocations (which fork
// `drift ssh-proxy`) can find the binary without absolute paths.
func (c *Circuit) DriftBinDir() string {
	c.t.Helper()
	bin := driftBinary(c.t)
	return filepath.Dir(bin)
}

// SSH runs the host's ssh(1) against the test circuit with the per-test
// HOME (so ~/.ssh/config is the harness-generated one) and a PATH that
// includes the compiled drift binary. Returns stdout, stderr, and the
// child's exit code. Transport failures (binary missing, etc.) fatal the
// test.
func (c *Circuit) SSH(ctx context.Context, args ...string) (stdout, stderr string, exitCode int) {
	c.t.Helper()
	// Explicit shim path: Go's exec.CommandContext does LookPath against the
	// parent's PATH, not cmd.Env, so plain "ssh" would still pick up the
	// system binary. The shim binary itself then delegates to /usr/bin/ssh
	// with -F <harness-config> so subprocess lookups find the right
	// configuration.
	cmd := osexec.CommandContext(ctx, filepath.Join(c.shimDir, "ssh"), args...)
	cmd.Env = overlayEnv(map[string]string{
		"HOME": c.driftHome,
		// Shim first so this ssh call goes through the harness config;
		// driftBinDir after so ProxyCommand forks of `drift` resolve.
		"PATH": c.shimDir +
			string(os.PathListSeparator) + c.DriftBinDir() +
			string(os.PathListSeparator) + os.Getenv("PATH"),
	})
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
	c.t.Fatalf("ssh %v: %v (stderr=%q)", args, err, stderr)
	return "", "", 1
}

// RegisterCircuit records the running container as a drift circuit and
// makes it the default. SSH config writes in drift are skipped — the
// harness appends a specific `Host drift.<name>` block here so the inner
// ssh hop from drift ssh-proxy (which runs `ssh drift.<circuit>`) resolves
// against a single matching block without stepping on the `Host drift.*.*`
// ProxyCommand pattern.
func (c *Circuit) RegisterCircuit(ctx context.Context, name string) {
	c.t.Helper()
	_, stderr, code := c.Drift(ctx, "circuit", "add", name,
		"--host", c.Target(),
		"--no-ssh-config",
		"--no-probe",
		"--default",
	)
	if code != 0 {
		c.t.Fatalf("drift circuit add %s: exit=%d stderr=%q", name, code, stderr)
	}
	cfgPath := filepath.Join(c.driftHome, ".ssh", "config")
	block := fmt.Sprintf(`
Host drift.%s
  HostName 127.0.0.1
  Port %d
  User %s
  IdentityFile %s
  IdentitiesOnly yes
  StrictHostKeyChecking no
  UserKnownHostsFile /dev/null
`, name, c.SSHPort, c.User, c.keyPath)
	f, err := os.OpenFile(cfgPath, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		c.t.Fatalf("open ssh config for append: %v", err)
	}
	defer f.Close()
	if _, err := f.WriteString(block); err != nil {
		c.t.Fatalf("append ssh config: %v", err)
	}
}

// dockerSocketGID returns the GID that owns /var/run/docker.sock so the
// harness can pass it to `docker run --group-add` and give the circuit user
// access to the mounted socket without --privileged or chmod.
func dockerSocketGID() (int, error) {
	fi, err := os.Stat("/var/run/docker.sock")
	if err != nil {
		return 0, err
	}
	sys, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 0, fmt.Errorf("unexpected stat type %T", fi.Sys())
	}
	return int(sys.Gid), nil
}

// randomHex returns 2*n hex chars of crypto-random data. Used to tag
// per-run resources (circuit container name, kart prefix) so the sweeper
// can find everything belonging to a given run.
func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("randomHex: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// sweepIntegrationContainers tears down any circuit or devpod workspace
// containers that belong to the integration test suite. It is called both
// before a test starts (to clean after a crashed earlier run) and after
// every test finishes via t.Cleanup.
//
// The sweep uses three filters:
//   - label=drift.integration=1 catches circuit containers tagged by
//     runContainer.
//   - name=<kartPrefix> catches any host-visible containers whose name
//     includes the kart prefix (currently a no-op since devpod hashes
//     workspace names, but harmless for belt-and-braces).
//   - label=dev.containers.id catches every devpod-built workspace in the
//     host daemon. We only apply this label sweep when a kartPrefix is
//     given so a stray devcontainer belonging to the outer project is not
//     wiped by a test-startup sweep.
//
// When kartPrefix is empty only the circuit-level sweep runs.
func sweepIntegrationContainers(ctx context.Context, t *testing.T, kartPrefix string) {
	t.Helper()
	filters := []string{"label=drift.integration=1"}
	if kartPrefix != "" {
		filters = append(filters,
			"name="+kartPrefix,
			"label=dev.containers.id",
		)
	}
	for _, f := range filters {
		out, err := osexec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", f).Output()
		if err != nil {
			t.Logf("sweep docker ps --filter %q: %v", f, err)
			continue
		}
		ids := strings.Fields(strings.TrimSpace(string(out)))
		if len(ids) == 0 {
			continue
		}
		args := append([]string{"rm", "-f"}, ids...)
		if out, err := osexec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
			t.Logf("sweep docker rm -f %v: %v\n%s", ids, err, out)
		}
	}
}

// overlayEnv returns a copy of os.Environ() with the given keys replaced.
// It exists because Go's exec package keeps duplicate keys as-is and the
// child process's libc getenv typically returns the first match — so
// appending HOME=… to os.Environ() silently leaves the parent's HOME in
// effect. overlayEnv removes existing entries for the overlay keys before
// appending the new values.
func overlayEnv(overlay map[string]string) []string {
	base := os.Environ()
	out := make([]string, 0, len(base)+len(overlay))
	for _, kv := range base {
		key := kv
		if eq := strings.IndexByte(kv, '='); eq >= 0 {
			key = kv[:eq]
		}
		if _, drop := overlay[key]; drop {
			continue
		}
		out = append(out, kv)
	}
	for k, v := range overlay {
		out = append(out, k+"="+v)
	}
	return out
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
