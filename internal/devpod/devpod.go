// Package devpod is lakitu's typed wrapper around the devpod CLI.
//
// Every call routes through [internal/exec.Run] so the process tree honors
// plans/PLAN.md § Critical invariants: context-cancellable, SIGTERM → SIGKILL
// after WaitDelay, no shell interposition, stdout/stderr captured separately.
// The drift client binary never imports this package — devpod runs only on
// the circuit, invoked by lakitu.
package devpod

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

// DefaultBinary is the devpod executable name looked up on $PATH when a
// [Client] is constructed without an override. Tests inject a fake via
// [Client.Binary] or [Client.Runner].
const DefaultBinary = "devpod"

// Runner is the execution seam. Production callers use [ExecRunner]; tests
// substitute a fake that returns canned stdout/stderr without spawning a
// real process. The signature mirrors [driftexec.Run] so the production
// adapter is a one-line passthrough.
type Runner interface {
	Run(ctx context.Context, cmd driftexec.Cmd) (driftexec.Result, error)
}

// RunnerFunc adapts a plain function to the [Runner] interface.
type RunnerFunc func(ctx context.Context, cmd driftexec.Cmd) (driftexec.Result, error)

// Run implements [Runner].
func (f RunnerFunc) Run(ctx context.Context, cmd driftexec.Cmd) (driftexec.Result, error) {
	return f(ctx, cmd)
}

// ExecRunner is the production [Runner] — a thin pass-through to
// [driftexec.Run]. Exposed as a value (not a method on Client) so callers
// that want to stack middlewares over the real runner can wrap it.
var ExecRunner Runner = RunnerFunc(driftexec.Run)

// Client is the typed interface to the devpod CLI. The zero value is usable
// and defaults to [ExecRunner] and [DefaultBinary].
type Client struct {
	// Binary overrides the devpod executable. Empty means [DefaultBinary].
	Binary string
	// Runner overrides the execution seam. nil means [ExecRunner].
	Runner Runner
	// Env, when non-nil, is passed as the child environment. nil inherits
	// the parent's env — the usual case for lakitu on a circuit.
	Env []string
}

func (c *Client) binary() string {
	if c == nil || c.Binary == "" {
		return DefaultBinary
	}
	return c.Binary
}

func (c *Client) runner() Runner {
	if c == nil || c.Runner == nil {
		return ExecRunner
	}
	return c.Runner
}

// run is the single place this package calls into exec. Every public method
// funnels through here so every devpod invocation inherits the same
// cancellation/shell/env invariants.
func (c *Client) run(ctx context.Context, args ...string) (driftexec.Result, error) {
	return c.runner().Run(ctx, driftexec.Cmd{
		Name: c.binary(),
		Args: args,
		Env:  c.envOrNil(),
	})
}

func (c *Client) envOrNil() []string {
	if c == nil || c.Env == nil {
		return nil
	}
	return append([]string(nil), c.Env...)
}

// UpOpts mirrors the subset of `devpod up` flags documented in
// plans/PLAN.md § "Useful devpod up flags" that lakitu composes from
// drift's own flags and tune presets.
type UpOpts struct {
	// Name is the workspace name (required). Passed as the positional
	// argument when Source is empty; otherwise Source is the positional
	// and --id=<Name> carries the name.
	Name string
	// Source is the positional source arg: a git URL, a local path, or
	// empty to reuse an existing workspace.
	Source string
	// Provider is the devpod provider (plans/PLAN.md fixes on "docker").
	// Empty means don't pass --provider.
	Provider string
	// IDE maps to --ide; plans/PLAN.md locks this at "none".
	IDE string
	// AdditionalFeatures is a JSON object serialized to
	// --additional-features. Pass the zero-value empty string to omit.
	AdditionalFeatures string
	// ExtraDevcontainerPath is the resolved on-disk path passed as
	// --extra-devcontainer-path. Callers are responsible for writing URLs
	// or inline JSON to a temp file first.
	ExtraDevcontainerPath string
	// Dotfiles is the layer-2 dotfiles repo URL (--dotfiles).
	Dotfiles string
	// DevcontainerImage overrides the container image (--devcontainer-image).
	DevcontainerImage string
	// FallbackImage is --fallback-image.
	FallbackImage string
	// GitCloneStrategy is --git-clone-strategy.
	GitCloneStrategy string
	// ConfigureSSH toggles --configure-ssh. Default: false. drift manages
	// its own SSH config and does not want devpod to edit ~/.ssh/config.
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

// Up invokes `devpod up` with the given options. Returns the combined stdout
// output for callers that want to log provisioning progress; stderr is
// available via the *Error on non-zero exit.
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

// Stop invokes `devpod stop <name>`. Idempotent at the devpod layer —
// stopping an already-stopped workspace is a no-op exit 0. lakitu relies on
// this for its own idempotency contract in plans/PLAN.md § Idempotency.
func (c *Client) Stop(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("devpod: Stop: name is required")
	}
	_, err := c.run(ctx, "stop", name)
	return err
}

// Delete invokes `devpod delete --force <name>`.
func (c *Client) Delete(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("devpod: Delete: name is required")
	}
	_, err := c.run(ctx, "delete", "--force", name)
	return err
}

// Status invokes `devpod status <name> --output json` and decodes the result.
// The status string is lower-cased before returning; empty strings are
// treated as [StatusNotFound] since some devpod versions emit that shape
// for missing workspaces.
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

// SSHOpts mirrors the useful subset of `devpod ssh` flags from
// plans/PLAN.md § "Useful devpod ssh flags".
type SSHOpts struct {
	// Name is the target workspace (required).
	Name string
	// Command runs inside the container instead of the default login shell.
	Command string
	// User is the container user (--user).
	User string
	// Workdir is the container-side working directory (--workdir).
	Workdir string
	// SendEnv forwards local env vars into the container (--send-env).
	SendEnv []string
	// SetEnv sets env vars inside the container (--set-env KEY=VALUE).
	SetEnv []string
	// KeepaliveInterval sets --ssh-keepalive-interval. Zero means default.
	KeepaliveInterval string
	// Stdio toggles --stdio, used by `drift ssh-proxy` to pipe the outer
	// OpenSSH handshake straight through to devpod's injected SSH server.
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

// SSH invokes `devpod ssh` with opts. Returns the captured stdout for
// non-interactive uses (`--command`); interactive sessions should drive
// stdio directly via a different transport — Phase 10's `drift connect`
// path does not go through this wrapper.
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

// Workspace is one entry returned by `devpod list --output json`. Fields are
// the subset lakitu needs to merge with its garage view. Unknown fields are
// tolerated — the wrapper does not DisallowUnknownFields here because
// devpod's JSON surface is additive and lakitu should keep working across
// minor upgrades.
type Workspace struct {
	ID       string `json:"id"`
	Source   Source `json:"source"`
	Provider struct {
		Name string `json:"name"`
	} `json:"provider"`
	LastUsed string `json:"lastUsed,omitempty"`
	Created  string `json:"creationTimestamp,omitempty"`
}

// Source describes how the workspace was created. Exactly one of the
// fields should be non-empty in a well-formed response; callers should
// prefer GitRepository when both are present.
type Source struct {
	GitRepository string `json:"gitRepository,omitempty"`
	LocalFolder   string `json:"localFolder,omitempty"`
	Image         string `json:"image,omitempty"`
}

// List invokes `devpod list --output json` and decodes the workspaces.
// An empty list is returned as an empty slice, never nil.
func (c *Client) List(ctx context.Context) ([]Workspace, error) {
	res, err := c.run(ctx, "list", "--output", "json")
	if err != nil {
		return nil, err
	}
	// devpod list emits `null` or `[]` on an empty garage depending on
	// version; normalize both to an empty slice.
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

// Logs invokes `devpod logs <name>` and returns the raw bytes. Streaming
// is deferred to a later phase — MVP's `kart.logs` returns a chunk.
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

// ExpectedVersion is the devpod fork/release drift was built against —
// injected at build time by the flake:
//
//	-X github.com/kurisu-agent/drift/internal/devpod.ExpectedVersion=v0.17.0
//
// Empty means "no pin" (dev builds); Verify then only reports what the
// circuit has without a match check. Keep the string in the same shape
// `devpod version` emits (with the leading "v").
var ExpectedVersion = ""

// VersionCheck is the result of comparing the circuit's live devpod
// version to ExpectedVersion. Callers (lakitu init, kart.new) decide what
// severity to render each state as.
type VersionCheck struct {
	Actual   string
	Expected string
	// Match is true when Expected is empty (no pin) or Actual == Expected.
	// Callers should treat Match=false as a warning, not a hard error —
	// devpod forks maintain backwards-compatible argv across minor bumps.
	Match bool
}

// Verify calls `devpod version` and compares to ExpectedVersion. A
// non-nil error means we couldn't determine the circuit's version at
// all (devpod binary missing, permissions, etc.).
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

// Version invokes `devpod version` and returns the trimmed one-line output
// (e.g. "v0.22.0"). Empty output yields an empty string — the caller
// decides whether that's an error.
func (c *Client) Version(ctx context.Context) (string, error) {
	res, err := c.run(ctx, "version")
	if err != nil {
		return "", err
	}
	return string(bytes.TrimSpace(res.Stdout)), nil
}

// ProviderList invokes `devpod provider list --output json` and returns
// the set of installed provider names. Order is unspecified.
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

// ProviderAdd invokes `devpod provider add <name>`. Callers should prefer
// EnsureProvider for idempotent add-if-missing flows.
func (c *Client) ProviderAdd(ctx context.Context, name string) error {
	if name == "" {
		return errors.New("devpod: ProviderAdd: name is required")
	}
	_, err := c.run(ctx, "provider", "add", name)
	return err
}

// EnsureProvider registers `name` via devpod if it isn't already listed.
// Safe to call on every init; a no-op when the provider is present.
// Returns true when a registration actually happened (useful for init
// summaries).
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

// InstallDotfiles invokes `devpod agent workspace install-dotfiles` with the
// given dotfiles URL (layer-1 plumbing used by Phase 8). A file:// URL is
// valid — lakitu writes the generated layer-1 script to a tmpdir and passes
// it here.
//
// The skevetter/devpod fork exposes this as `--repository <url>`. Upstream
// devpod used `--dotfiles`; we track the fork (see flake.nix devpodPin) so
// --repository is the right flag. If a future fork bump renames again, a
// thin fallback here would be cleaner than forcing every lakitu rebuild.
func (c *Client) InstallDotfiles(ctx context.Context, url string) error {
	if url == "" {
		return errors.New("devpod: InstallDotfiles: url is required")
	}
	_, err := c.run(ctx, "agent", "workspace", "install-dotfiles", "--repository", url)
	return err
}

// trimJSONSpace strips ASCII whitespace from both ends of a raw JSON buffer
// without the extra allocation [strings.TrimSpace] would incur for a byte
// slice. Kept inline so the package has no stdlib imports beyond what's
// already present.
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
