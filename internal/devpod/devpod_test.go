package devpod_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kurisu-agent/drift/internal/devpod"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

// fakeRunner captures every invocation and replays canned results. Each
// slot matches one Run call, in order. Missing slots cause the test to
// fail so we never silently paper over an unexpected extra exec.
type fakeRunner struct {
	calls  []driftexec.Cmd
	replay []fakeReply
	idx    int
}

type fakeReply struct {
	stdout string
	stderr string
	err    error
}

func (f *fakeRunner) Run(_ context.Context, cmd driftexec.Cmd) (driftexec.Result, error) {
	f.calls = append(f.calls, cmd)
	if f.idx >= len(f.replay) {
		return driftexec.Result{}, errors.New("fakeRunner: unexpected extra call")
	}
	r := f.replay[f.idx]
	f.idx++
	res := driftexec.Result{Stdout: []byte(r.stdout), Stderr: []byte(r.stderr)}
	return res, r.err
}

func newClient(runner driftexec.Runner) *devpod.Client {
	return &devpod.Client{Binary: "devpod", Runner: runner}
}

func TestUpArgsPropagateAllFlags(t *testing.T) {
	t.Parallel()
	f := &fakeRunner{replay: []fakeReply{{stdout: "ok"}}}
	c := newClient(f)

	out, err := c.Up(t.Context(), devpod.UpOpts{
		Name:                  "proj",
		Source:                "git@github.com:user/repo.git",
		Provider:              "docker",
		IDE:                   "none",
		AdditionalFeatures:    `{"ghcr.io/x/y:1":{}}`,
		ExtraDevcontainerPath: "/tmp/dc.json",
		Dotfiles:              "https://example.com/dots.git",
		DevcontainerImage:     "alpine",
		FallbackImage:         "ubuntu",
		GitCloneStrategy:      "treeless",
		WorkspaceEnv:          []string{"WS=1"},
		DotfilesScriptEnv:     []string{"BUILD=2"},
		ConfigureSSH:          false,
	})
	if err != nil {
		t.Fatalf("Up: %v", err)
	}
	if string(out) != "ok" {
		t.Errorf("Up stdout = %q, want %q", out, "ok")
	}
	if len(f.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(f.calls))
	}
	got := f.calls[0]
	want := driftexec.Cmd{
		Name: "devpod",
		Args: []string{
			"up",
			"--provider", "docker",
			"--ide", "none",
			"--additional-features", `{"ghcr.io/x/y:1":{}}`,
			"--extra-devcontainer-path", "/tmp/dc.json",
			"--dotfiles", "https://example.com/dots.git",
			"--devcontainer-image", "alpine",
			"--fallback-image", "ubuntu",
			"--git-clone-strategy", "treeless",
			"--workspace-env", "WS=1",
			"--dotfiles-script-env", "BUILD=2",
			"--id", "proj",
			"git@github.com:user/repo.git",
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("cmd mismatch (-want +got):\n%s", diff)
	}
}

// TestClientMirrorEchoesArgvAndWiresStreamMirrors verifies the verbose-mode
// surface: a one-line argv echo lands on Mirror before each spawn (with
// embedded URL creds redacted), and the runner Cmd carries non-nil
// MirrorStdout/MirrorStderr wrappers so the child's live output streams
// through with the same redaction applied to devpod's own log lines.
func TestClientMirrorEchoesArgvAndWiresStreamMirrors(t *testing.T) {
	t.Parallel()
	f := &fakeRunner{replay: []fakeReply{{stdout: "ok"}}}
	var mirror bytes.Buffer
	c := &devpod.Client{Binary: "devpod", Runner: f, Mirror: &mirror}

	if _, err := c.Up(t.Context(), devpod.UpOpts{
		Name:     "proj",
		Source:   "https://ghp_secret@github.com/o/r.git",
		Provider: "docker",
	}); err != nil {
		t.Fatalf("Up: %v", err)
	}

	echo := mirror.String()
	if !strings.Contains(echo, "→ devpod up") {
		t.Errorf("argv echo missing prefix: %q", echo)
	}
	if strings.Contains(echo, "ghp_secret") {
		t.Errorf("PAT leaked into argv echo: %q", echo)
	}
	if !strings.Contains(echo, "[REDACTED]@github.com") {
		t.Errorf("URL creds not redacted: %q", echo)
	}

	cmd := f.calls[0]
	if cmd.MirrorStdout == nil || cmd.MirrorStderr == nil {
		t.Errorf("Cmd mirrors not wired: stdout=%v stderr=%v", cmd.MirrorStdout, cmd.MirrorStderr)
	}
	// Writing a PAT-bearing log line through the wired stdout mirror must
	// land in the underlying Mirror with the URL creds redacted — covers
	// the "devpod itself prints a URL with embedded PAT" case that the
	// argv-only redaction wouldn't catch.
	mirror.Reset()
	if _, err := cmd.MirrorStdout.Write([]byte("Cloning https://ghp_xxx@github.com/o/r.git\n")); err != nil {
		t.Fatalf("write to wrapped mirror: %v", err)
	}
	got := mirror.String()
	if strings.Contains(got, "ghp_xxx") {
		t.Errorf("PAT leaked through stream mirror: %q", got)
	}
	if !strings.Contains(got, "[REDACTED]@github.com") {
		t.Errorf("stream mirror didn't redact: %q", got)
	}
}

// TestClientNilMirrorIsQuiet covers the default path: no Mirror configured
// → no argv echo, no Cmd mirror fields populated. Guards against a future
// refactor accidentally turning verbose mode on by default.
func TestClientNilMirrorIsQuiet(t *testing.T) {
	t.Parallel()
	f := &fakeRunner{replay: []fakeReply{{stdout: "ok"}}}
	c := newClient(f) // Mirror unset

	if _, err := c.Up(t.Context(), devpod.UpOpts{Name: "proj", Provider: "docker"}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	cmd := f.calls[0]
	if cmd.MirrorStdout != nil || cmd.MirrorStderr != nil {
		t.Errorf("Cmd mirrors set without Client.Mirror: stdout=%v stderr=%v", cmd.MirrorStdout, cmd.MirrorStderr)
	}
}

func TestUpNameOnlyUsesPositionalName(t *testing.T) {
	t.Parallel()
	f := &fakeRunner{replay: []fakeReply{{stdout: ""}}}
	c := newClient(f)

	if _, err := c.Up(t.Context(), devpod.UpOpts{Name: "proj", Provider: "docker"}); err != nil {
		t.Fatalf("Up: %v", err)
	}
	args := f.calls[0].Args
	want := []string{"up", "--provider", "docker", "proj"}
	if diff := cmp.Diff(want, args); diff != "" {
		t.Errorf("args mismatch (-want +got):\n%s", diff)
	}
}

func TestUpRequiresName(t *testing.T) {
	t.Parallel()
	c := newClient(&fakeRunner{})
	if _, err := c.Up(t.Context(), devpod.UpOpts{}); err == nil {
		t.Fatal("Up with empty Name returned nil, want error")
	}
}

func TestStopDeleteLogsInstallDotfilesHappyPath(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		call func(c *devpod.Client, ctx context.Context) error
		args []string
	}{
		{
			name: "stop",
			call: func(c *devpod.Client, ctx context.Context) error { return c.Stop(ctx, "proj") },
			args: []string{"stop", "proj"},
		},
		{
			name: "delete",
			call: func(c *devpod.Client, ctx context.Context) error { return c.Delete(ctx, "proj") },
			args: []string{"delete", "--force", "proj"},
		},
		{
			name: "install_dotfiles",
			call: func(c *devpod.Client, ctx context.Context) error {
				return c.InstallDotfilesWithOpts(ctx, devpod.InstallDotfilesOpts{URL: "file:///tmp/layer1.sh"})
			},
			args: []string{"agent", "workspace", "install-dotfiles", "--repository", "file:///tmp/layer1.sh"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := &fakeRunner{replay: []fakeReply{{}}}
			c := newClient(f)
			if err := tc.call(c, t.Context()); err != nil {
				t.Fatalf("call: %v", err)
			}
			if diff := cmp.Diff(tc.args, f.calls[0].Args); diff != "" {
				t.Errorf("args mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStatusDecodesJSONAndNormalizes(t *testing.T) {
	t.Parallel()

	cases := map[string]devpod.Status{
		`{"state":"Running"}`:  devpod.StatusRunning,
		`{"state":"stopped"}`:  devpod.StatusStopped,
		`{"state":"Busy"}`:     devpod.StatusBusy,
		`{"state":"Starting"}`: devpod.StatusBusy,
		`{"state":"NotFound"}`: devpod.StatusNotFound,
		`{"state":""}`:         devpod.StatusNotFound,
		`{"state":"garbled"}`:  devpod.StatusError,
	}
	for payload, want := range cases {
		t.Run(string(want)+"_"+payload, func(t *testing.T) {
			t.Parallel()
			f := &fakeRunner{replay: []fakeReply{{stdout: payload}}}
			c := newClient(f)
			got, err := c.Status(t.Context(), "proj")
			if err != nil {
				t.Fatalf("Status: %v", err)
			}
			if got != want {
				t.Errorf("Status = %q, want %q", got, want)
			}
			if diff := cmp.Diff([]string{"status", "proj", "--output", "json"}, f.calls[0].Args); diff != "" {
				t.Errorf("args mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestStatusPropagatesExecError(t *testing.T) {
	t.Parallel()
	f := &fakeRunner{replay: []fakeReply{{err: &driftexec.Error{Name: "devpod", ExitCode: 1, FirstStderrLine: "no such"}}}}
	c := newClient(f)
	if _, err := c.Status(t.Context(), "proj"); err == nil {
		t.Fatal("Status with exec error returned nil")
	}
}

func TestListDecodesWorkspaces(t *testing.T) {
	t.Parallel()
	payload := `[
		{"id":"alpha","source":{"gitRepository":"https://github.com/a/a.git"},"provider":{"name":"docker"}},
		{"id":"beta","source":{"localFolder":"/home/u/proj"},"provider":{"name":"docker"}}
	]`
	f := &fakeRunner{replay: []fakeReply{{stdout: payload}}}
	c := newClient(f)
	got, err := c.List(t.Context())
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 || got[0].ID != "alpha" || got[1].ID != "beta" {
		t.Fatalf("List = %+v, want [alpha beta]", got)
	}
	if got[0].Source.GitRepository != "https://github.com/a/a.git" {
		t.Errorf("alpha source = %+v", got[0].Source)
	}
	if diff := cmp.Diff([]string{"list", "--output", "json"}, f.calls[0].Args); diff != "" {
		t.Errorf("args mismatch (-want +got):\n%s", diff)
	}
}

func TestListNullAndEmptyTreatedAsEmptySlice(t *testing.T) {
	t.Parallel()
	for _, body := range []string{"null", "", "[]", "  \n"} {
		t.Run("body="+body, func(t *testing.T) {
			t.Parallel()
			f := &fakeRunner{replay: []fakeReply{{stdout: body}}}
			c := newClient(f)
			got, err := c.List(t.Context())
			if err != nil {
				t.Fatalf("List(%q): %v", body, err)
			}
			if got == nil {
				t.Errorf("List returned nil slice, want empty")
			}
			if len(got) != 0 {
				t.Errorf("List returned %d entries, want 0", len(got))
			}
		})
	}
}

func TestListInvalidJSONReturnsError(t *testing.T) {
	t.Parallel()
	f := &fakeRunner{replay: []fakeReply{{stdout: "{ not really json"}}}
	c := newClient(f)
	if _, err := c.List(t.Context()); err == nil {
		t.Fatal("List with bad JSON returned nil")
	}
}

func TestSSHArgsAndStdioFlag(t *testing.T) {
	t.Parallel()
	f := &fakeRunner{replay: []fakeReply{{stdout: "hello"}}}
	c := newClient(f)

	out, err := c.SSH(t.Context(), devpod.SSHOpts{
		Name:              "proj",
		Command:           "echo hi",
		User:              "vscode",
		Workdir:           "/workspaces/proj",
		SendEnv:           []string{"GIT_TOKEN"},
		SetEnv:            []string{"FOO=bar"},
		KeepaliveInterval: "30s",
	})
	if err != nil {
		t.Fatalf("SSH: %v", err)
	}
	if string(out) != "hello" {
		t.Errorf("SSH stdout = %q", out)
	}
	want := []string{
		"ssh", "proj",
		"--command", "echo hi",
		"--user", "vscode",
		"--workdir", "/workspaces/proj",
		"--send-env", "GIT_TOKEN",
		"--set-env", "FOO=bar",
		"--ssh-keepalive-interval", "30s",
	}
	if diff := cmp.Diff(want, f.calls[0].Args); diff != "" {
		t.Errorf("ssh args mismatch (-want +got):\n%s", diff)
	}

	// Stdio variant for drift ssh-proxy.
	f2 := &fakeRunner{replay: []fakeReply{{}}}
	c2 := newClient(f2)
	if _, err := c2.SSH(t.Context(), devpod.SSHOpts{Name: "proj", Stdio: true}); err != nil {
		t.Fatalf("SSH stdio: %v", err)
	}
	if diff := cmp.Diff([]string{"ssh", "proj", "--stdio"}, f2.calls[0].Args); diff != "" {
		t.Errorf("stdio args mismatch (-want +got):\n%s", diff)
	}
}

func TestSSHRequiresName(t *testing.T) {
	t.Parallel()
	c := newClient(&fakeRunner{})
	if _, err := c.SSH(t.Context(), devpod.SSHOpts{}); err == nil {
		t.Fatal("SSH with empty name returned nil")
	}
}

func TestClientHonorsBinaryOverride(t *testing.T) {
	t.Parallel()
	f := &fakeRunner{replay: []fakeReply{{stdout: "[]"}}}
	c := &devpod.Client{Binary: "/custom/devpod-path", Runner: f}
	if _, err := c.List(t.Context()); err != nil {
		t.Fatalf("List: %v", err)
	}
	if f.calls[0].Name != "/custom/devpod-path" {
		t.Errorf("binary = %q, want /custom/devpod-path", f.calls[0].Name)
	}
}

func TestClientDefaultsToExecRunner(t *testing.T) {
	t.Parallel()
	// Using an intentionally missing binary so the call fails without
	// actually executing anything, but still proves ExecRunner is wired.
	c := &devpod.Client{Binary: "definitely-not-a-real-binary-xyzzy"}
	_, err := c.List(t.Context())
	if err == nil {
		t.Fatal("List with missing binary returned nil")
	}
	if !strings.Contains(err.Error(), "definitely-not-a-real-binary-xyzzy") {
		t.Errorf("err = %v, want binary name in message", err)
	}
}

func TestNoShellInvocationInSources(t *testing.T) {
	t.Parallel()
	// Phase 5's internal/exec has its own grep test for shell literals.
	// Replicating it here guards the devpod wrapper against someone
	// accidentally shelling out from this package in the future.
	for _, path := range []string{"devpod.go", "status.go"} {
		src, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", path, err)
		}
		text := string(src)
		for _, banned := range []string{`"sh"`, `"bash"`, `"/bin/sh"`, `"/bin/bash"`, `"-c"`} {
			if strings.Contains(text, banned) {
				t.Errorf("%s: contains forbidden shell token %s", path, banned)
			}
		}
	}
}
