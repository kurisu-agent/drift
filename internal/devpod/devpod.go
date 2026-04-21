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

	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

// osEnviron is a package-level indirection so tests that stub the runner
// don't need to manipulate the real process env.
var osEnviron = os.Environ

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
	// Mirror, if non-nil, receives a one-line argv echo before each spawn
	// (with embedded URL creds redacted) AND a live copy of the child's
	// stdout+stderr. Wire to os.Stderr in lakitu's verbose mode so SSH
	// relays devpod progress to the drift client in real time.
	Mirror io.Writer
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
	c.echoArgv(args)
	return c.runner().Run(ctx, driftexec.Cmd{
		Name:         c.binary(),
		Args:         args,
		Env:          c.envOrNil(),
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
	return &redactingMirror{w: c.Mirror}
}

// redactingMirror line-buffers writes and runs each completed line
// through driftexec.RedactSecrets before forwarding. ANSI escapes pass
// through (colors preserved). A trailing partial line (no \n) buffers
// until the next write completes it; for devpod, every log entry ends
// in a newline so this is fine in practice.
type redactingMirror struct {
	w   io.Writer
	buf []byte
}

func (m *redactingMirror) Write(p []byte) (int, error) {
	m.buf = append(m.buf, p...)
	for {
		idx := bytes.IndexByte(m.buf, '\n')
		if idx < 0 {
			break
		}
		line := m.buf[:idx+1]
		if _, err := io.WriteString(m.w, driftexec.RedactSecrets(string(line))); err != nil {
			return 0, err
		}
		m.buf = m.buf[idx+1:]
	}
	return len(p), nil
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
	if c == nil || c.Env == nil {
		return nil
	}
	return append([]string(nil), c.Env...)
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
	if o.Source != "" {
		args = append(args, "--id", o.Name, o.Source)
	} else {
		args = append(args, o.Name)
	}
	return args, nil
}

func (c *Client) Up(ctx context.Context, opts UpOpts) ([]byte, error) {
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
	res, err := c.run(ctx, args...)
	if err != nil {
		return nil, err
	}
	return res.Stdout, nil
}

// Workspace is the subset of `devpod list --output json` that lakitu merges
// with its garage view. DisallowUnknownFields is deliberately NOT set —
// devpod's JSON surface is additive and we want to ride through minor bumps.
type Workspace struct {
	ID       string `json:"id"`
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
	trimmed := trimJSONSpace(res.Stdout)
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
	trimmed := trimJSONSpace(res.Stdout)
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

// InstallDotfilesOpts carries optional configuration for InstallDotfiles.
// ProcessEnv is set as additional environment on the local devpod
// invocation; values forwarded this way live for the lifetime of that
// single process — they never land in the container's containerEnv.
type InstallDotfilesOpts struct {
	URL        string
	ProcessEnv []string
}

// InstallDotfiles invokes `devpod agent workspace install-dotfiles`. A
// file:// URL is valid — lakitu writes the generated layer-1 script to a
// tmpdir and passes it here.
//
// The skevetter/devpod fork renamed --dotfiles to --repository; flake.nix
// pins the fork so this flag is correct. A future rename would motivate a
// fallback probe.
func (c *Client) InstallDotfiles(ctx context.Context, url string) error {
	return c.InstallDotfilesWithOpts(ctx, InstallDotfilesOpts{URL: url})
}

// InstallDotfilesWithOpts extends InstallDotfiles with extra per-invocation
// process env — used to deliver chest-backed build-time secrets to the
// install-dotfiles process (e.g. a GITHUB_TOKEN for a private
// dotfiles_repo clone) without writing them anywhere on disk or into the
// container's persistent env.
func (c *Client) InstallDotfilesWithOpts(ctx context.Context, opts InstallDotfilesOpts) error {
	if opts.URL == "" {
		return errors.New("devpod: InstallDotfiles: url is required")
	}
	env := c.envOrNil()
	if len(opts.ProcessEnv) > 0 {
		// Inherit the parent env (usual case: Client.Env is nil) so PATH,
		// HOME, TMPDIR, DEVPOD_HOME still reach the child — then layer the
		// secret env on top so a name collision lets the caller override.
		if env == nil {
			env = append(env, osEnviron()...)
		}
		env = append(env, opts.ProcessEnv...)
	}
	args := []string{"agent", "workspace", "install-dotfiles", "--repository", opts.URL}
	c.echoArgv(args)
	_, err := c.runner().Run(ctx, driftexec.Cmd{
		Name:         c.binary(),
		Args:         args,
		Env:          env,
		MirrorStdout: c.streamMirror(),
		MirrorStderr: c.streamMirror(),
	})
	return err
}

func trimJSONSpace(b []byte) []byte {
	start, end := 0, len(b)
	for start < end {
		c := b[start]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		start++
	}
	for end > start {
		c := b[end-1]
		if c != ' ' && c != '\t' && c != '\n' && c != '\r' {
			break
		}
		end--
	}
	return b[start:end]
}
