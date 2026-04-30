// Package devpod is lakitu's typed wrapper around the devpod CLI. Every
// call routes through [internal/exec.Run] so the process tree honors the
// cancellation/signal/shell invariants. Not imported by the drift client.
package devpod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"
	"strings"
	"sync"

	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

// EnvOrNilForTest exposes envOrNil to the external test package so
// tests can assert the DEVPOD_HOME injection without a full exec
// roundtrip.
func EnvOrNilForTest(c *Client) []string { return c.envOrNil() }

// MarkContextOptionsAppliedForTest marks ensureContextOptions as
// already-fired so external tests exercising Up don't have to
// pre-arm their fakeRunner with an extra reply for the implicit
// `devpod context set-options` spawn. Production code never calls
// this.
func MarkContextOptionsAppliedForTest(c *Client) { c.ensureCtxOnce.Do(func() {}) }

// IsNotInstalled reports whether err came from devpod being absent on PATH
// (as opposed to a real devpod-side failure). Callers use it to render one
// actionable install hint instead of two copies of os/exec's nested wrap.
func IsNotInstalled(err error) bool {
	return errors.Is(err, osexec.ErrNotFound)
}

func InstallHint() string {
	ver := ExpectedVersion
	if ver == "" {
		ver = "latest"
	}
	return fmt.Sprintf(
		"curl -fsSL https://github.com/skevetter/devpod/releases/%s/download/devpod-linux-amd64 | sudo install /dev/stdin /usr/local/bin/devpod",
		tagOrLatest(ver),
	)
}

// GitHub's release download URL is `/releases/latest/download/…`
// OR `/releases/download/<tag>/…`.
func tagOrLatest(ver string) string {
	if ver == "" || ver == "latest" {
		return "latest"
	}
	return "download/" + ver
}

const DefaultBinary = "devpod"

// Client is the typed interface to the devpod CLI. The zero value is usable.
type Client struct {
	Binary string
	Runner driftexec.Runner
	// Env: nil inherits the parent env (the usual lakitu case on a circuit).
	Env []string
	// ensureCtxOnce gates the one-shot `devpod context set-options` call
	// that disables host-side credential injection. Fires before the
	// first Up; subsequent Ups skip the spawn.
	ensureCtxOnce sync.Once
	ensureCtxErr  error
	// Mirror, if non-nil, receives a one-line argv echo before each spawn
	// (with embedded URL creds redacted) AND a live copy of the child's
	// stdout+stderr. Wire to os.Stderr in lakitu's verbose mode so SSH
	// relays devpod progress to the drift client in real time.
	Mirror io.Writer
	// DevpodHome, if non-empty, is injected as DEVPOD_HOME=<val> into
	// each child's env — even when Env is nil (the usual "inherit
	// parent" path). Zero value preserves the historical behavior: the
	// child sees whatever DEVPOD_HOME the parent process has (typically
	// unset, meaning devpod's own ~/.devpod/ default). Lakitu sets this
	// to config.DriftDevpodHome() so drift-managed workspaces live in
	// ~/.drift/devpod/, fully isolated from the user's ~/.devpod/.
	DevpodHome string
}

func (c *Client) binary() string {
	if c == nil || c.Binary == "" {
		return DefaultBinary
	}
	return c.Binary
}

func (c *Client) runner() driftexec.Runner {
	if c == nil || c.Runner == nil {
		return driftexec.DefaultRunner
	}
	return c.Runner
}

func (c *Client) run(ctx context.Context, args ...string) (driftexec.Result, error) {
	return c.runWithEnv(ctx, c.envOrNil(), args, nil)
}

// runWithEnv is the single place every devpod spawn lands, with an
// explicit env slice. Use this over c.run when the call needs one-shot
// process env so the argv-echo/mirror wiring stays centralized. stdin,
// when non-nil, is wired to the child's stdin — used by SSH to pipe
// secret-bearing scripts to `bash -s` without exposing them on argv.
func (c *Client) runWithEnv(ctx context.Context, env []string, args []string, stdin io.Reader) (driftexec.Result, error) {
	c.echoArgv(args)
	return c.runner().Run(ctx, driftexec.Cmd{
		Name:         c.binary(),
		Args:         args,
		Env:          env,
		Stdin:        stdin,
		MirrorStdout: c.streamMirror(),
		MirrorStderr: c.streamMirror(),
	})
}

// streamMirror returns a per-call, per-stream redacting wrapper around
// c.Mirror. Devpod's own log lines often print full URLs with embedded
// PATs (from --dotfiles) — wrapping ensures the same redaction the argv
// echo gets. Separate instances per stream avoid races on the line
// buffer when os/exec's stdout/stderr copy goroutines fire concurrently.
func (c *Client) streamMirror() io.Writer {
	if c == nil || c.Mirror == nil {
		return nil
	}
	return &driftexec.RedactingWriter{W: c.Mirror}
}

// echoArgv writes a one-line `→ devpod <args>` summary to Mirror before
// each spawn. Each arg goes through driftexec.RedactSecrets so embedded
// URL creds (e.g. https://<pat>@github.com/...) don't end up in the
// operator's terminal. Writes directly to c.Mirror (not the redacting
// wrapper) since the line is already redacted and ends in a newline.
func (c *Client) echoArgv(args []string) {
	if c == nil || c.Mirror == nil {
		return
	}
	parts := make([]string, 0, len(args)+1)
	parts = append(parts, c.binary())
	for _, a := range args {
		parts = append(parts, driftexec.RedactSecrets(a))
	}
	fmt.Fprintf(c.Mirror, "→ %s\n", strings.Join(parts, " "))
}

func (c *Client) envOrNil() []string {
	if c == nil {
		return nil
	}
	if c.DevpodHome == "" {
		if c.Env == nil {
			return nil
		}
		return append([]string(nil), c.Env...)
	}
	// DevpodHome is a default, not an override. An operator or the
	// integration harness may set DEVPOD_HOME upstream (e.g. to a
	// bind-mounted path that docker-on-host can resolve); respect that
	// setting and only inject ours when nothing's been declared
	// upstream.
	var base []string
	if c.Env != nil {
		base = append(base, c.Env...)
	} else {
		base = append(base, os.Environ()...)
	}
	for _, e := range base {
		if strings.HasPrefix(e, "DEVPOD_HOME=") {
			return base
		}
	}
	return append(base, "DEVPOD_HOME="+c.DevpodHome)
}

type UpOpts struct {
	Name string
	// Source: git URL, local path, or empty to reuse an existing workspace.
	Source                string
	Provider              string
	IDE                   string
	AdditionalFeatures    string
	ExtraDevcontainerPath string
	Dotfiles              string
	DevcontainerImage     string
	FallbackImage         string
	GitCloneStrategy      string
	// WorkspaceEnv renders as --workspace-env KEY=VALUE pairs; each entry
	// becomes part of the container's containerEnv for the workspace's
	// lifetime. (SSHOpts.SetEnv uses --set-env because devpod ssh has its
	// own flag with that name; devpod up does not.)
	WorkspaceEnv []string
	// DotfilesScriptEnv renders as --dotfiles-script-env KEY=VALUE pairs.
	// Each entry is exposed only to the dotfiles install script that
	// devpod runs from --dotfiles, then gone — never lands in containerEnv.
	// Used to pass build-time secrets (e.g. a PAT for cloning a private
	// dotfiles_repo) without persisting them in the workspace.
	DotfilesScriptEnv []string
	// ConfigureSSH: drift manages its own SSH config and keeps this off.
	ConfigureSSH bool
	// Recreate renders `--recreate`, forcing devpod to tear down and
	// rebuild the workspace (devcontainer reprocessed, image rebuilt,
	// container recreated). Used by kart.recreate for devcontainer.json
	// changes and by kart.rebuild when re-applying tune drift. Without
	// it, `devpod up` on an existing workspace reuses the running
	// container.
	Recreate bool
}

func (o UpOpts) args() ([]string, error) {
	if o.Name == "" {
		return nil, errors.New("devpod: UpOpts.Name is required")
	}
	args := []string{"up"}
	if o.Provider != "" {
		args = append(args, "--provider", o.Provider)
	}
	if o.IDE != "" {
		args = append(args, "--ide", o.IDE)
	}
	if o.AdditionalFeatures != "" {
		args = append(args, "--additional-features", o.AdditionalFeatures)
	}
	if o.ExtraDevcontainerPath != "" {
		args = append(args, "--extra-devcontainer-path", o.ExtraDevcontainerPath)
	}
	if o.Dotfiles != "" {
		args = append(args, "--dotfiles", o.Dotfiles)
	}
	if o.DevcontainerImage != "" {
		args = append(args, "--devcontainer-image", o.DevcontainerImage)
	}
	if o.FallbackImage != "" {
		args = append(args, "--fallback-image", o.FallbackImage)
	}
	if o.GitCloneStrategy != "" {
		args = append(args, "--git-clone-strategy", o.GitCloneStrategy)
	}
	for _, v := range o.WorkspaceEnv {
		args = append(args, "--workspace-env", v)
	}
	for _, v := range o.DotfilesScriptEnv {
		args = append(args, "--dotfiles-script-env", v)
	}
	if o.ConfigureSSH {
		args = append(args, "--configure-ssh")
	}
	if o.Recreate {
		args = append(args, "--recreate")
	}
	if o.Source != "" {
		args = append(args, "--id", o.Name, o.Source)
	} else {
		args = append(args, o.Name)
	}
	return args, nil
}

func (c *Client) Up(ctx context.Context, opts UpOpts) ([]byte, error) {
	if err := c.ensureContextOptions(ctx); err != nil {
		return nil, err
	}
	args, err := opts.args()
	if err != nil {
		return nil, err
	}
	res, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return res.Stdout, nil
}

// ensureContextOptions persists the devpod context options drift relies
// on, fired once per Client lifetime before the first Up. Drift owns
// per-kart identity / auth via the post-up `ghAuthFragment` (per-
// character user.name / user.email and, when a PAT is set, gh auth
// login --with-token plus gh's credential helper), so devpod's SSH-
// time credential helper would otherwise paper that over with the
// lakitu host's git credentials. Forcing SSH_INJECT_GIT_CREDENTIALS
// =false at devpod-context level once per RPC turns the helper off.
//
// This option only suppresses the SSH-time helper. Devpod's agent
// still copies the host's `[user]` block into the kart's home
// gitconfig at workspace setup time (devpod 0.22.0 has no separate
// option to gate that). The post-up `ghAuthFragment` overwrites it
// with the character's identity, so the kart ends up with the right
// `[user]` block regardless of what devpod left behind.
//
// Idempotent at devpod's level: set-options just rewrites
// ~/.drift/devpod/config.yaml. Fast enough that paying the cost on
// every fresh Client is fine.
func (c *Client) ensureContextOptions(ctx context.Context) error {
	c.ensureCtxOnce.Do(func() {
		opts := []string{
			"SSH_INJECT_GIT_CREDENTIALS=false",
		}
		for _, kv := range opts {
			if _, err := c.run(ctx, "context", "set-options", "-o", kv); err != nil {
				c.ensureCtxErr = fmt.Errorf("devpod: ensure context option %q: %w", kv, err)
				return
			}
		}
	})
	return c.ensureCtxErr
}

func (c *Client) Stop(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("devpod: Stop: name is required")
	}
	_, err := c.run(ctx, "stop", name)
	return err
}

func (c *Client) Delete(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("devpod: Delete: name is required")
	}
	_, err := c.run(ctx, "delete", "--force", name)
	return err
}

func (c *Client) Status(ctx context.Context, name string) (Status, error) {
	if name == "" {
		return "", errors.New("devpod: Status: name is required")
	}
	res, err := c.run(ctx, "status", name, "--output", "json")
	if err != nil {
		return "", err
	}
	var payload struct {
		State string `json:"state"`
	}
	if jerr := json.Unmarshal(res.Stdout, &payload); jerr != nil {
		return "", fmt.Errorf("devpod: parse status for %q: %w", name, jerr)
	}
	return normalizeStatus(payload.State), nil
}

type SSHOpts struct {
	Name    string
	Command string
	User    string
	Workdir string
	SendEnv []string
	SetEnv  []string
	// KeepaliveInterval: zero falls back to devpod's default.
	KeepaliveInterval string
	// Stdio: used by `drift ssh-proxy` to pipe the outer OpenSSH handshake
	// straight through to devpod's injected SSH server.
	Stdio bool
	// Stdin, when non-nil, is wired to devpod ssh's stdin. The data
	// flows over the SSH session to the in-container shell — pair with
	// Command="bash -s" (or similar) to execute a script delivered
	// here without exposing its bytes on lakitu's argv table. Plain
	// stdin pass-through; no wrapping or transformation.
	Stdin io.Reader
	// NoStartServices passes `--start-services=false`, suppressing
	// devpod's auto-forward of the kart's declared `forwardPorts`
	// (and its git/docker credentials helper). Set this for any
	// `--command` invocation that doesn't need the side helpers —
	// otherwise devpod tries to bind workstation-side ports on the
	// circuit (where lakitu runs), which collides with whatever is
	// already there and dumps `bind: address already in use` into
	// stderr. drift's ports reconcile owns forwards explicitly, so
	// devpod's auto-forward is just noise we'd rather not propagate.
	NoStartServices bool
}

func (o SSHOpts) args() ([]string, error) {
	if o.Name == "" {
		return nil, errors.New("devpod: SSHOpts.Name is required")
	}
	args := []string{"ssh", o.Name}
	if o.Command != "" {
		args = append(args, "--command", o.Command)
	}
	if o.User != "" {
		args = append(args, "--user", o.User)
	}
	if o.Workdir != "" {
		args = append(args, "--workdir", o.Workdir)
	}
	for _, v := range o.SendEnv {
		args = append(args, "--send-env", v)
	}
	for _, v := range o.SetEnv {
		args = append(args, "--set-env", v)
	}
	if o.KeepaliveInterval != "" {
		args = append(args, "--ssh-keepalive-interval", o.KeepaliveInterval)
	}
	if o.Stdio {
		args = append(args, "--stdio")
	}
	if o.NoStartServices {
		args = append(args, "--start-services=false")
	}
	return args, nil
}

// SSH invokes `devpod ssh` and returns captured stdout — for non-interactive
// uses only. Interactive sessions (drift connect) drive stdio via a separate
// transport that does not go through this wrapper.
func (c *Client) SSH(ctx context.Context, opts SSHOpts) ([]byte, error) {
	args, err := opts.args()
	if err != nil {
		return nil, err
	}
	res, err := c.runWithEnv(ctx, c.envOrNil(), args, opts.Stdin)
	if err != nil {
		return nil, err
	}
	return res.Stdout, nil
}

// Workspace is the subset of `devpod list --output json` that lakitu merges
// with its garage view. DisallowUnknownFields is deliberately NOT set —
// devpod's JSON surface is additive and we want to ride through minor bumps.
type Workspace struct {
	ID string `json:"id"`
	// UID is devpod's stable internal handle (e.g. "default-dr-e7cdc"),
	// emitted on every workspace by `devpod list`. lakitu uses it to
	// match workspaces against the `dev.containers.id` docker label so
	// container state can be looked up in one `docker ps` instead of N
	// `devpod status` shells.
	UID      string `json:"uid,omitempty"`
	Source   Source `json:"source"`
	Provider struct {
		Name string `json:"name"`
	} `json:"provider"`
	LastUsed string `json:"lastUsed,omitempty"`
	Created  string `json:"creationTimestamp,omitempty"`
}

// Source: exactly one field should be non-empty. Prefer GitRepository when
// both are present.
type Source struct {
	GitRepository string `json:"gitRepository,omitempty"`
	LocalFolder   string `json:"localFolder,omitempty"`
	Image         string `json:"image,omitempty"`
}

func (c *Client) List(ctx context.Context) ([]Workspace, error) {
	res, err := c.run(ctx, "list", "--output", "json")
	if err != nil {
		return nil, err
	}
	// devpod emits `null` or `[]` on an empty garage depending on version.
	trimmed := bytes.TrimSpace(res.Stdout)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return []Workspace{}, nil
	}
	var workspaces []Workspace
	if err := json.Unmarshal(res.Stdout, &workspaces); err != nil {
		return nil, fmt.Errorf("devpod: parse list output: %w", err)
	}
	if workspaces == nil {
		workspaces = []Workspace{}
	}
	return workspaces, nil
}

func (c *Client) Logs(ctx context.Context, name string) ([]byte, error) {
	if name == "" {
		return nil, errors.New("devpod: Logs: name is required")
	}
	res, err := c.run(ctx, "logs", name)
	if err != nil {
		return nil, err
	}
	return res.Stdout, nil
}

// ExpectedVersion is the devpod fork/release drift was built against,
// injected by the flake's -X ldflag. Empty means "no pin" (dev builds);
// Verify then only reports what the circuit has without a match check.
var ExpectedVersion = ""

type VersionCheck struct {
	Actual   string
	Expected string
	// Match: true when Expected is empty (no pin) or Actual == Expected.
	// Callers treat Match=false as a warning, not a hard error.
	Match bool
}

func (c *Client) Verify(ctx context.Context) (VersionCheck, error) {
	got, err := c.Version(ctx)
	if err != nil {
		return VersionCheck{Expected: ExpectedVersion}, err
	}
	return VersionCheck{
		Actual:   got,
		Expected: ExpectedVersion,
		Match:    ExpectedVersion == "" || got == ExpectedVersion,
	}, nil
}

func (c *Client) Version(ctx context.Context) (string, error) {
	res, err := c.run(ctx, "version")
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(res.Stdout)), nil
}

func (c *Client) ProviderList(ctx context.Context) ([]string, error) {
	res, err := c.run(ctx, "provider", "list", "--output", "json")
	if err != nil {
		return nil, err
	}
	trimmed := bytes.TrimSpace(res.Stdout)
	if len(trimmed) == 0 || string(trimmed) == "null" {
		return []string{}, nil
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(res.Stdout, &m); err != nil {
		return nil, fmt.Errorf("devpod: parse provider list: %w", err)
	}
	names := make([]string, 0, len(m))
	for n := range m {
		names = append(names, n)
	}
	return names, nil
}

func (c *Client) ProviderAdd(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("devpod: ProviderAdd: name is required")
	}
	_, err := c.run(ctx, "provider", "add", name)
	return err
}

// EnsureProvider registers `name` if absent. Returns true when a
// registration actually happened (useful for init summaries).
func (c *Client) EnsureProvider(ctx context.Context, name string) (added bool, err error) {
	have, err := c.ProviderList(ctx)
	if err != nil {
		return false, err
	}
	for _, h := range have {
		if h == name {
			return false, nil
		}
	}
	if err := c.ProviderAdd(ctx, name); err != nil {
		return false, err
	}
	return true, nil
}
