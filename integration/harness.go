//go:build integration

// Package integration hosts the tier-2 test harness that drives drift
// against a real sshd + lakitu running in a container. Build-tag-gated so
// `go test ./...` stays fast; `make integration` is the entry point.
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
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// CircuitName is the canonical circuit alias used by integration tests.
// StartReadyCircuit registers the circuit under this name so downstream
// drift.<name> ssh aliases and probe assertions have a shared constant.
const CircuitName = "test"

// Package-scope build artifacts. TestMain compiles drift + lakitu once into
// a shared tmp dir and sets these paths; helpers read them via their Once
// guards. The docker image build is also memoized because every
// `StartCircuit` would otherwise pay a daemon round-trip for an identical
// tag.
var (
	pkgTmpDir string

	driftBinOnce sync.Once
	driftBinPath string
	driftBinErr  error

	lakituBinOnce sync.Once
	lakituBinPath string
	lakituBinErr  error

	imageOnce sync.Once
	imageErr  error
)

type Circuit struct {
	t *testing.T

	ContainerID string
	SSHPort     int
	User        string
	WorkDir     string

	keyPath        string
	sshConfigPath  string
	driftConfigDir string
	driftHome      string
	shimDir        string

	// runID tags the container and kart names so orphaned state from a
	// crashed run can be swept by label.
	runID         string
	containerName string
	kartPrefix    string
	// sharedScratch exists at the same path on both the devcontainer and
	// the circuit (bind-mount). Used as the circuit's TMPDIR so drift
	// scratch dirs created there are resolvable by the outer dockerd when
	// devpod bind-mounts the source.
	sharedScratch string
}

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

	// Pre-test sweep: tear down devpod containers left by a crashed earlier
	// run before we spend time building images.
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
		// Kill devpod workspace containers we spawned before the circuit
		// itself — otherwise they'd be orphaned with no ~/.devpod state.
		sweepIntegrationContainers(bg, t, c.kartPrefix)
		c.Stop(bg)
	})
	return c
}

// StartReadyCircuit bundles the three steps almost every integration test
// runs after StartCircuit: `lakitu init` on the circuit, then
// `drift circuit add` under the canonical [circuitName]. Tests that need to
// exercise the registration flow itself (probe_test, drift_test) still call
// StartCircuit directly.
//
// If withRecorder is true, the devpod recorder shim is installed and the
// returned *DevpodRecorder is non-nil.
func StartReadyCircuit(ctx context.Context, t *testing.T, withRecorder bool) (*Circuit, *DevpodRecorder) {
	t.Helper()
	c := StartCircuit(ctx, t)
	if err := SSHCommand(ctx, c, "lakitu", "init"); err != nil {
		t.Fatalf("lakitu init: %v", err)
	}
	c.RegisterCircuit(ctx, CircuitName)
	if withRecorder {
		return c, c.InstallDevpodRecorder(ctx)
	}
	return c, nil
}

// TestCtx returns a context with the given deadline and registers its
// cancel with t.Cleanup so tests don't have to `defer cancel()`. Prefer
// this over hand-rolled context.WithTimeout sites.
func TestCtx(t *testing.T, d time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), d)
	t.Cleanup(cancel)
	return ctx
}

func (c *Circuit) Drift(ctx context.Context, args ...string) (stdout, stderr string, exitCode int) {
	c.t.Helper()
	bin := driftBinary(c.t)
	cmd := osexec.CommandContext(ctx, bin, args...)
	cmd.Env = overlayEnv(map[string]string{
		"HOME":            c.driftHome,
		"XDG_CONFIG_HOME": filepath.Join(c.driftHome, ".config"),
		// Prepend the harness shim dir so every `ssh` drift spawns routes
		// through the harness-managed ssh_config (see writeSSHShim).
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

func (c *Circuit) Target() string {
	return fmt.Sprintf("%s@127.0.0.1:%d", c.User, c.SSHPort)
}

func SSHCommand(ctx context.Context, c *Circuit, name string, args ...string) error {
	argv := append([]string{"exec", "-u", c.User, c.ContainerID, name}, args...)
	return run(ctx, "docker", argv...)
}

func (c *Circuit) Stop(ctx context.Context) {
	if c == nil || c.ContainerID == "" {
		return
	}
	id := c.ContainerID
	c.ContainerID = ""
	_ = run(ctx, "docker", "rm", "-f", id)
}

func (c *Circuit) generateKey() {
	c.t.Helper()
	cmd := osexec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-C", "drift integration test", "-f", c.keyPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		c.t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
}

// buildImage stages the circuit image used by every StartCircuit. The
// lakitu binary and the docker build are each guarded by a sync.Once — the
// image tag is identical across tests and building it more than once per
// `go test` run just burns daemon RTT.
func (c *Circuit) buildImage(ctx context.Context) {
	c.t.Helper()
	lakituBin, err := lakituBinary(ctx, c.t)
	if err != nil {
		c.t.Fatalf("build lakitu: %v", err)
	}
	ctxDir := filepath.Join(pkgTmpDir, "docker-ctx")
	imageOnce.Do(func() {
		if err := os.MkdirAll(ctxDir, 0o755); err != nil {
			imageErr = fmt.Errorf("mkdir docker ctx: %w", err)
			return
		}
		if err := copyFile(lakituBin, filepath.Join(ctxDir, "lakitu")); err != nil {
			imageErr = fmt.Errorf("stage lakitu: %w", err)
			return
		}
		if err := copyFile(filepath.Join(repoRoot(c.t), "integration", "Dockerfile.circuit"), filepath.Join(ctxDir, "Dockerfile")); err != nil {
			imageErr = fmt.Errorf("stage dockerfile: %w", err)
			return
		}
		if out, err := osexec.CommandContext(ctx, "docker", "build",
			"-t", "drift-integration-circuit",
			"--build-arg", "LAKITU_PATH=./lakitu",
			ctxDir,
		).CombinedOutput(); err != nil {
			imageErr = fmt.Errorf("docker build: %w\n%s", err, out)
			return
		}
	})
	if imageErr != nil {
		c.t.Fatalf("build circuit image: %v", imageErr)
	}
}

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

	// Share a per-run dir at the SAME path on devcontainer + circuit so
	// that when drift creates /tmp/drift-kart-<id>/ on the circuit and
	// hands the path to devpod, the outer dockerd can resolve it as a
	// bind-mount source against its own filesystem. TMPDIR is propagated
	// to ssh sessions via PermitUserEnvironment + ~/.ssh/environment.
	c.sharedScratch = "/tmp/drift-int-scratch-" + c.runID
	if err := os.MkdirAll(c.sharedScratch, 0o777); err != nil {
		c.t.Fatalf("mkdir shared scratch: %v", err)
	}
	// MkdirAll applies the parent's umask, so the mode arg is a ceiling, not
	// a guarantee. GitHub Actions runners are UID 1001; the circuit user is
	// UID 1000. An 0o755 dir owned by the runner blocks writes from the
	// circuit, so chmod 0o777 is required for devpod-inside-circuit to
	// populate DEVPOD_HOME. Local devcontainer happens to run UID 1000
	// (matching circuit), which masked the bug.
	if err := os.Chmod(c.sharedScratch, 0o777); err != nil {
		c.t.Fatalf("chmod shared scratch: %v", err)
	}
	c.t.Cleanup(func() { _ = os.RemoveAll(c.sharedScratch) })
	devpodHome := c.sharedScratch + "/.devpod-home"
	if err := os.MkdirAll(devpodHome, 0o777); err != nil {
		c.t.Fatalf("mkdir devpod home: %v", err)
	}
	if err := os.Chmod(devpodHome, 0o777); err != nil {
		c.t.Fatalf("chmod devpod home: %v", err)
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
		// DEVPOD_HOME via `docker run -e` so both `docker exec` setup paths
		// and sshd-spawned sessions see the same value. A single source of
		// truth avoids provider registrations vanishing between the two.
		"-e", "DEVPOD_HOME=" + devpodHome,
		// Pin lakitu's devpod binary to the one the Dockerfile installed
		// under /usr/local/bin. Without this, lakitu's EnsurePinned would
		// download its own copy under ~/.drift/bin/devpod, shadowing the
		// shim-based tests (InstallDevpodShim / InstallDevpodRecorder)
		// which overwrite /usr/local/bin/devpod.
		"-e", "DRIFT_DEVPOD_BINARY=/usr/local/bin/devpod",
		"--group-add", strconv.Itoa(dockerGID),
		"drift-integration-circuit",
	}
	out, err := osexec.CommandContext(ctx, "docker", args...).Output()
	if err != nil {
		c.t.Fatalf("docker run: %v", err)
	}
	c.ContainerID = strings.TrimSpace(string(out))

	// Push authorized_keys post-start; baking it into the image would
	// cache the key in a layer and leak across tests.
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

	// Pin TMPDIR/DEVPOD_HOME/DRIFT_DEVPOD_BINARY for ssh sessions. Sshd-spawned
	// sessions don't inherit PID 1's env, so the docker run -e above isn't
	// enough on its own — ~/.ssh/environment covers the ssh login path (which
	// is how drift's RPC transport spawns lakitu on the circuit).
	envLine := "TMPDIR=" + c.sharedScratch + "\n" +
		"DEVPOD_HOME=" + devpodHome + "\n" +
		"DRIFT_DEVPOD_BINARY=/usr/local/bin/devpod\n"
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

	// sshd's initgroups() resets supplementary groups to /etc/group at
	// login, so the circuit user only inherits --group-add's docker GID
	// inside sshd itself — not inside sessions. Add the user to a group
	// whose GID matches the mounted socket's owner so `docker ps` from a
	// login shell can talk to the outer daemon.
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
			_ = conn.Close()
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	c.t.Fatalf("sshd never became reachable on 127.0.0.1:%d", c.SSHPort)
}

// writeSSHConfig emits three blocks:
//   - Host drift.*.* → ProxyCommand + identity; HostName intentionally
//     absent so %h in ProxyCommand expands to the original alias.
//   - Match host drift.*,!drift.*.* → HostName/Port for bare circuit
//     aliases, excluding drift.*.* to keep %h untouched for ProxyCommand.
//   - Host <test>-target → plain direct-connect alias for raw SSH tests.
//
// StrictHostKeyChecking is off because the host key regenerates every run.
func (c *Circuit) writeSSHConfig() {
	c.t.Helper()
	dir := filepath.Join(c.driftHome, ".ssh")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		c.t.Fatalf("mkdir .ssh: %v", err)
	}
	// The `Host 127.0.0.1` block covers `drift circuit add`'s raw probe:
	// it ssh's directly to `user@127.0.0.1` (no drift.<name> alias yet)
	// so we need a matching block that supplies the identity file and
	// turns off strict host checking — without it the probe hangs on the
	// host-key prompt.
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

Host 127.0.0.1
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

// LakituRPC shells a JSON-RPC request into `lakitu rpc` via `docker exec -i`.
// Used for method surfaces drift's CLI doesn't expose directly (chest.*,
// character.*), keeping integration coverage close to the RPC catalog.
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

func (c *Circuit) KartName(suffix string) string {
	return c.kartPrefix + suffix
}

// writeSSHShim installs an ssh wrapper that forces -F <harness-config>.
// OpenSSH locates ~/.ssh/config via getpwuid(getuid()) rather than $HOME,
// so setting HOME alone doesn't redirect config for subprocesses (drift
// ssh-proxy's nested ssh, rpc/client.SSHTransport, warmup probe). Tests
// that want the real ssh can invoke /usr/bin/ssh directly.
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

// StageStarter creates a bare git repo at /tmp/<name>.git inside the
// circuit with one commit from `files` and returns its file:// URL.
// Deterministic, offline source for drift --starter tests.
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
		// Heredoc so backtick/quote content in body survives unchanged.
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

// StageCloneFixture stages /srv/gitrepos/<name>.git as a file:// URL.
// DEVPOD_HOME under sharedScratch ensures devpod v0.22's agent clone
// writes into a path the outer dockerd can resolve when bind-mounting.
// The bare repo is chowned to UID 1000 so git's dubious-ownership guard
// doesn't trip when the circuit user clones.
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
	setup.WriteString("chown -R 1000:1000 /srv/gitrepos\n")
	setup.WriteString("chmod -R a+rX /srv/gitrepos\n")
	// -u root: staging writes under /srv/gitrepos (circuit user can't
	// create there). The chown hands the final tree back to UID 1000.
	if err := run(ctx, "docker", "exec", "-u", "root", c.ContainerID, "sh", "-c", setup.String()); err != nil {
		c.t.Fatalf("stage clone fixture %s: %v", name, err)
	}
	return "file://" + bare
}

// InstallDevpodShim overwrites /usr/local/bin/devpod with a test shim.
// The integration image lacks a docker daemon; tests that need `devpod
// ssh --stdio` semantics fake just enough of the subcommand surface.
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

// DevpodInvocation captures one recorded `devpod …` call. ArtifactDir is
// the path inside the circuit where the shim preserved file/dir args
// (starter source, layer-1 dotfiles, extra-devcontainer file) so tests
// can inspect what drift materialized. Env is the process env at
// invocation time — used by env-injection tests.
type DevpodInvocation struct {
	Argv        []string `json:"argv"`
	ArtifactDir string   `json:"artifact_dir,omitempty"`
	Env         []string `json:"env,omitempty"`
}

type DevpodRecorder struct {
	c *Circuit
}

// InstallDevpodRecorder compiles the in-tree shim and installs it as
// /usr/local/bin/devpod. The shim emits canned JSON for status (Running)
// and list (empty) so drift's kart.info/kart.list paths keep working;
// everything else exits 0 and appends to /tmp/devpod-invocations.log.
func (c *Circuit) InstallDevpodRecorder(ctx context.Context) *DevpodRecorder {
	c.t.Helper()
	binPath := filepath.Join(c.WorkDir, "devpod-shim")
	build := osexec.CommandContext(ctx, "go", "build", "-o", binPath, "./integration/shim/devpod")
	build.Dir = repoRoot(c.t)
	build.Env = overlayEnv(map[string]string{
		"CGO_ENABLED": "0",
		"GOOS":        "linux",
		"GOARCH":      runtime.GOARCH,
	})
	if out, err := build.CombinedOutput(); err != nil {
		c.t.Fatalf("build devpod shim: %v\n%s", err, out)
	}
	if err := run(ctx, "docker", "cp", binPath, c.ContainerID+":/usr/local/bin/devpod"); err != nil {
		c.t.Fatalf("docker cp devpod shim: %v", err)
	}
	if err := run(ctx, "docker", "exec", c.ContainerID, "chmod", "0755", "/usr/local/bin/devpod"); err != nil {
		c.t.Fatalf("chmod devpod shim: %v", err)
	}
	_ = run(ctx, "docker", "exec", c.ContainerID, "rm", "-f", "/tmp/devpod-invocations.log")
	return &DevpodRecorder{c: c}
}

func (r *DevpodRecorder) Invocations(ctx context.Context) []DevpodInvocation {
	r.c.t.Helper()
	out, err := osexec.CommandContext(ctx, "docker", "exec", r.c.ContainerID,
		"cat", "/tmp/devpod-invocations.log").Output()
	if err != nil {
		// Missing log = no calls yet; docker exec emits non-zero for a
		// missing file, distinguishable by ExitError.
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

func (r *DevpodRecorder) FindUp(ctx context.Context) *DevpodInvocation {
	for _, inv := range r.Invocations(ctx) {
		if len(inv.Argv) > 0 && inv.Argv[0] == "up" {
			cp := inv
			return &cp
		}
	}
	return nil
}

func (r *DevpodRecorder) FindInstallDotfiles(ctx context.Context) *DevpodInvocation {
	for _, inv := range r.Invocations(ctx) {
		if len(inv.Argv) >= 3 && inv.Argv[0] == "agent" && inv.Argv[2] == "install-dotfiles" {
			cp := inv
			return &cp
		}
	}
	return nil
}

// FindAllUps collects every `devpod up` invocation across the recorder
// log. Used to compare successive kart.new / kart.restart --workspace-env
// sets.
func (r *DevpodRecorder) FindAllUps(ctx context.Context) []DevpodInvocation {
	var out []DevpodInvocation
	for _, inv := range r.Invocations(ctx) {
		if len(inv.Argv) > 0 && inv.Argv[0] == "up" {
			out = append(out, inv)
		}
	}
	return out
}

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

func (c *Circuit) ListArtifact(ctx context.Context, inv *DevpodInvocation, relDir string) []string {
	c.t.Helper()
	if inv == nil || inv.ArtifactDir == "" {
		c.t.Fatalf("ListArtifact: invocation has no artifact_dir")
	}
	full := filepath.ToSlash(filepath.Join(inv.ArtifactDir, relDir))
	// -A: include dotfile entries (e.g. .git in a stripped starter) but
	// skip . and ..
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

func (c *Circuit) DriftBinDir() string {
	c.t.Helper()
	bin := driftBinary(c.t)
	return filepath.Dir(bin)
}

// DriftBin returns the path to the test-built drift binary. Exposed for
// callers that need to spawn drift under a PTY rather than through the
// captured-stdio helper (see Drift).
func DriftBin(t *testing.T) string {
	t.Helper()
	return driftBinary(t)
}

// DriftEnv returns the env overlay Drift() uses (HOME pinned to the
// per-test drift home, PATH prefixed with the ssh shim + drift bin dir,
// XDG_CONFIG_HOME mirrored). Callers assembling their own *exec.Cmd —
// e.g. PTY-driven interactive tests — should reuse this so drift lands
// on the same config/ssh wiring as the scripted Drift helper.
func DriftEnv(c *Circuit) []string {
	c.t.Helper()
	return overlayEnv(map[string]string{
		"HOME":            c.driftHome,
		"XDG_CONFIG_HOME": filepath.Join(c.driftHome, ".config"),
		"PATH": c.shimDir + string(os.PathListSeparator) +
			c.DriftBinDir() + string(os.PathListSeparator) +
			os.Getenv("PATH"),
		// A real terminal emulator would set TERM; xterm is widely understood
		// by the bubbletea renderer the huh prompts use.
		"TERM": "xterm-256color",
	})
}

// SSH runs host ssh(1) against the circuit with the per-test HOME and a
// PATH that includes the compiled drift binary (so ProxyCommand forks of
// `drift ssh-proxy` resolve).
func (c *Circuit) SSH(ctx context.Context, args ...string) (stdout, stderr string, exitCode int) {
	c.t.Helper()
	// Explicit shim path: Go's exec.CommandContext does LookPath against
	// the parent's PATH, not cmd.Env, so plain "ssh" would pick up the
	// system binary instead of the shim.
	cmd := osexec.CommandContext(ctx, filepath.Join(c.shimDir, "ssh"), args...)
	// XDG_CONFIG_HOME must be pinned alongside HOME — otherwise a parent-env
	// value leaks through to the ProxyCommand's `drift ssh-proxy`, which
	// then reads the wrong config.yaml and reports circuit_not_found even
	// though drift circuit add wrote to the per-test HOME. Keep this in sync
	// with Drift() above, where the same overlay is applied.
	cmd.Env = overlayEnv(map[string]string{
		"HOME":            c.driftHome,
		"XDG_CONFIG_HOME": filepath.Join(c.driftHome, ".config"),
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

// RegisterCircuit records the container as a drift circuit named `name`
// and makes it the default.
//
// The circuit's canonical name now comes from the server, so the harness
// first pushes `name` via `lakitu config set name <name>` (the container's
// hostname is an unpredictable Docker ID and would fall back to
// "circuit"). Then `drift circuit add <target>` probes server.info,
// discovers the name we just set, and writes the local config under it.
//
// The appended Host drift.<name> block exists so drift ssh-proxy's inner
// hop (`ssh drift.<circuit>`) resolves against a single matching block
// without stepping on the Host drift.*.* ProxyCommand.
func (c *Circuit) RegisterCircuit(ctx context.Context, name string) {
	c.t.Helper()
	if err := SSHCommand(ctx, c, "lakitu", "config", "set", "name", name); err != nil {
		c.t.Fatalf("lakitu config set name: %v", err)
	}
	_, stderr, code := c.Drift(ctx, "circuit", "add",
		c.Target(),
		"--no-ssh-config",
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
	defer func() { _ = f.Close() }()
	if _, err := f.WriteString(block); err != nil {
		c.t.Fatalf("append ssh config: %v", err)
	}
}

// ArgvHas reports whether flag is present in argv followed by value.
// Example: ArgvHas(argv, "--additional-features", `{"a":1}`).
func ArgvHas(argv []string, flag, value string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && argv[i+1] == value {
			return true
		}
	}
	return false
}

// ArgvValue returns the value immediately following flag, or "" if absent.
func ArgvValue(argv []string, flag string) string {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag {
			return argv[i+1]
		}
	}
	return ""
}

// ArgvHasValuePrefix reports whether the flag-value pair has a value that
// starts with prefix. Used where the value's suffix is a chest-resolved
// secret — callers assert presence/absence of the KEY= half, not the
// literal value.
func ArgvHasValuePrefix(argv []string, flag, prefix string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && strings.HasPrefix(argv[i+1], prefix) {
			return true
		}
	}
	return false
}

// EnvHas reports whether env contains the exact KEY=VALUE pair.
func EnvHas(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// DevcontainerIDs returns the unique dev.containers.id values present on
// the outer docker daemon — one per devpod-managed workspace regardless of
// the generated container name.
func DevcontainerIDs(ctx context.Context, t *testing.T) map[string]struct{} {
	t.Helper()
	out, err := osexec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label=dev.containers.id",
		"--format", "{{.Label \"dev.containers.id\"}}").Output()
	if err != nil {
		t.Fatalf("docker ps dev.containers.id: %v", err)
	}
	ids := map[string]struct{}{}
	for _, id := range strings.Fields(strings.TrimSpace(string(out))) {
		ids[id] = struct{}{}
	}
	return ids
}

// SetDiff returns the members of a not present in b.
func SetDiff(a, b map[string]struct{}) []string {
	var out []string
	for k := range a {
		if _, ok := b[k]; !ok {
			out = append(out, k)
		}
	}
	return out
}

// SetDiffSet returns the members of a not present in b as a set.
func SetDiffSet(a, b map[string]struct{}) map[string]struct{} {
	out := map[string]struct{}{}
	for k := range a {
		if _, ok := b[k]; !ok {
			out[k] = struct{}{}
		}
	}
	return out
}

// WorkspaceContainerName resolves a dev.containers.id label back to the
// container name docker exec can target. Returns "" if no container matches.
func WorkspaceContainerName(ctx context.Context, t *testing.T, devContainerID string) string {
	t.Helper()
	out, err := osexec.CommandContext(ctx, "docker", "ps",
		"--filter", "label=dev.containers.id="+devContainerID,
		"--format", "{{.Names}}").Output()
	if err != nil {
		t.Fatalf("docker ps for label %q: %v", devContainerID, err)
	}
	name := strings.TrimSpace(string(out))
	if name == "" || strings.ContainsRune(name, '\n') {
		// Either no match or multiple — the first-line fallback handles the
		// latter since devpod typically builds one container per workspace.
		fields := strings.Fields(name)
		if len(fields) > 0 {
			return fields[0]
		}
		return ""
	}
	return name
}

// DockerExec runs a command inside a container and returns stdout. Used to
// poke at workspace filesystems without routing through devpod ssh (which
// would need a full shell session in a -t pty context).
func DockerExec(ctx context.Context, container string, args ...string) ([]byte, error) {
	argv := append([]string{"exec", container}, args...)
	cmd := osexec.CommandContext(ctx, "docker", argv...)
	out, err := cmd.Output()
	if err != nil {
		if ee, ok := err.(*osexec.ExitError); ok {
			return nil, fmt.Errorf("docker exec %s %v: %w (stderr=%q)", container, args, err, ee.Stderr)
		}
		return nil, err
	}
	return out, nil
}

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

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic("randomHex: " + err.Error())
	}
	return hex.EncodeToString(b)
}

// sweepIntegrationContainers tears down circuit + devpod workspace
// containers belonging to the test suite. Called both before a test (to
// clean after a crashed earlier run) and after every test via t.Cleanup.
//
// Two passes:
//  1. label=drift.integration=1 — circuit containers tagged by runContainer.
//  2. devpod-built workspace containers whose `dev.containers.id` label
//     starts with kartPrefix. Devpod sets that label to the workspace ID
//     (see GetDockerLabelForID upstream), and our test workspaces are
//     created with names beginning `int-<runID>-`, so the prefix uniquely
//     identifies our spawned containers. An earlier version filtered by
//     bare `label=dev.containers.id` and so swept *every* devcontainer on
//     the daemon — happily `docker rm -f`'ing the user's own kart if it
//     happened to be running. Don't reintroduce that.
func sweepIntegrationContainers(ctx context.Context, t *testing.T, kartPrefix string) {
	t.Helper()
	ids := dockerPSIDs(ctx, t, "label=drift.integration=1")
	if kartPrefix != "" {
		ids = append(ids, devpodWorkspaceIDsWithPrefix(ctx, t, kartPrefix)...)
	}
	if len(ids) == 0 {
		return
	}
	args := append([]string{"rm", "-f"}, ids...)
	if out, err := osexec.CommandContext(ctx, "docker", args...).CombinedOutput(); err != nil {
		t.Logf("sweep docker rm -f %v: %v\n%s", ids, err, out)
	}
}

func dockerPSIDs(ctx context.Context, t *testing.T, filter string) []string {
	t.Helper()
	out, err := osexec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", filter).Output()
	if err != nil {
		t.Logf("sweep docker ps --filter %q: %v", filter, err)
		return nil
	}
	return strings.Fields(strings.TrimSpace(string(out)))
}

// devpodWorkspaceIDsWithPrefix returns container IDs for devpod-built
// containers whose `dev.containers.id` label value starts with prefix.
// Docker's --filter doesn't support prefix matching on label values, so we
// list candidates with --format and trim by prefix in Go.
func devpodWorkspaceIDsWithPrefix(ctx context.Context, t *testing.T, prefix string) []string {
	t.Helper()
	const format = `{{.ID}} {{.Label "dev.containers.id"}}`
	out, err := osexec.CommandContext(ctx, "docker", "ps", "-a",
		"--filter", "label=dev.containers.id",
		"--format", format).Output()
	if err != nil {
		t.Logf("sweep dev.containers.id list: %v", err)
		return nil
	}
	var ids []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if strings.HasPrefix(fields[1], prefix) {
			ids = append(ids, fields[0])
		}
	}
	return ids
}

// overlayEnv replaces keys in os.Environ() rather than appending. Go's exec
// package keeps duplicate keys as-is and libc getenv returns the first
// match, so plain append silently leaves the parent's value in effect.
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
	defer func() { _ = l.Close() }()
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

// driftBinary returns the path to the compiled drift binary, building it
// once per package run into pkgTmpDir. Subsequent callers hit the cached
// path.
func driftBinary(t *testing.T) string {
	t.Helper()
	driftBinOnce.Do(func() {
		bin := filepath.Join(pkgTmpDir, "drift")
		build := osexec.Command("go", "build", "-o", bin, "./cmd/drift")
		build.Dir = repoRoot(t)
		if out, err := build.CombinedOutput(); err != nil {
			driftBinErr = fmt.Errorf("build drift: %w\n%s", err, out)
			return
		}
		driftBinPath = bin
	})
	if driftBinErr != nil {
		t.Fatalf("%v", driftBinErr)
	}
	return driftBinPath
}

// lakituBinary returns the path to a cross-compiled lakitu binary built
// once per package run under pkgTmpDir. GOARCH honors runtime.GOARCH so an
// arm64 Docker Desktop VM on an Apple Silicon host gets a native binary
// (the prior "amd64 on non-linux" override produced an x86_64 binary that
// the arm64 VM couldn't execute).
func lakituBinary(ctx context.Context, t *testing.T) (string, error) {
	t.Helper()
	lakituBinOnce.Do(func() {
		bin := filepath.Join(pkgTmpDir, "lakitu")
		build := osexec.CommandContext(ctx, "go", "build", "-o", bin, "./cmd/lakitu")
		build.Dir = repoRoot(t)
		build.Env = overlayEnv(map[string]string{
			"CGO_ENABLED": "0",
			"GOOS":        "linux",
			"GOARCH":      runtime.GOARCH,
		})
		if out, err := build.CombinedOutput(); err != nil {
			lakituBinErr = fmt.Errorf("go build lakitu: %w\n%s", err, out)
			return
		}
		lakituBinPath = bin
	})
	return lakituBinPath, lakituBinErr
}

func run(ctx context.Context, name string, args ...string) error {
	cmd := osexec.CommandContext(ctx, name, args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("%s %v: %w\n%s", name, args, err, out)
	}
	return nil
}

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
