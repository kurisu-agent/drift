package drift

import (
	"bytes"
	"reflect"
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/model"
)

func TestExpandOwnerRepoShorthand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name        string
		in          newCmd
		wantName    string
		wantClone   string
		wantStarter string
	}{
		{
			name:      "owner/repo shorthand expands",
			in:        newCmd{Name: "kurisu-agent/drift"},
			wantName:  "drift",
			wantClone: "https://github.com/kurisu-agent/drift",
		},
		{
			name:      "plain name passes through",
			in:        newCmd{Name: "myproject"},
			wantName:  "myproject",
			wantClone: "",
		},
		{
			name:      "explicit --clone suppresses shorthand",
			in:        newCmd{Name: "owner/repo", Clone: "https://example.com/x"},
			wantName:  "owner/repo",
			wantClone: "https://example.com/x",
		},
		{
			name:        "explicit --starter suppresses shorthand",
			in:          newCmd{Name: "owner/repo", Starter: "https://example.com/starter"},
			wantName:    "owner/repo",
			wantStarter: "https://example.com/starter",
		},
		{
			name:     "three-segment slug passes through untouched",
			in:       newCmd{Name: "a/b/c"},
			wantName: "a/b/c",
		},
		{
			name:     "empty owner passes through untouched",
			in:       newCmd{Name: "/repo"},
			wantName: "/repo",
		},
		{
			name:     "empty repo passes through untouched",
			in:       newCmd{Name: "owner/"},
			wantName: "owner/",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := tc.in
			expandOwnerRepoShorthand(&cmd)
			if cmd.Name != tc.wantName {
				t.Errorf("Name = %q, want %q", cmd.Name, tc.wantName)
			}
			if cmd.Clone != tc.wantClone {
				t.Errorf("Clone = %q, want %q", cmd.Clone, tc.wantClone)
			}
			if cmd.Starter != tc.wantStarter {
				t.Errorf("Starter = %q, want %q", cmd.Starter, tc.wantStarter)
			}
		})
	}
}

func TestExpandCloneShorthand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		in        string
		wantClone string
	}{
		{name: "owner/repo expands", in: "kurisu-agent/drift", wantClone: "https://github.com/kurisu-agent/drift"},
		{name: "https URL passes through", in: "https://github.com/owner/repo", wantClone: "https://github.com/owner/repo"},
		{name: "scp-style ssh URL passes through", in: "git@github.com:owner/repo", wantClone: "git@github.com:owner/repo"},
		{name: "absolute path passes through", in: "/tmp/repo", wantClone: "/tmp/repo"},
		{name: "relative path passes through", in: "./repo", wantClone: "./repo"},
		{name: "home path passes through", in: "~/code/repo", wantClone: "~/code/repo"},
		{name: "three segments pass through", in: "a/b/c", wantClone: "a/b/c"},
		{name: "empty owner passes through", in: "/repo", wantClone: "/repo"},
		{name: "empty repo passes through", in: "owner/", wantClone: "owner/"},
		{name: "empty input stays empty", in: "", wantClone: ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := newCmd{Clone: tc.in}
			expandCloneShorthand(&cmd)
			if cmd.Clone != tc.wantClone {
				t.Errorf("Clone = %q, want %q", cmd.Clone, tc.wantClone)
			}
		})
	}
}

func TestShouldAutoConnect(t *testing.T) {
	t.Parallel()

	// stdinIsTTY returns false for non-*os.File readers, which is the
	// realistic signal for scripted/piped invocations — exactly the case
	// where auto-connect must stay off.
	pipeStdin := &bytes.Buffer{}

	cases := []struct {
		name string
		cmd  newCmd
		root CLI
		want bool
	}{
		{
			name: "--no-connect disables even on a TTY",
			cmd:  newCmd{Connect: false},
			root: CLI{Output: "text"},
			want: false,
		},
		{
			name: "JSON output suppresses auto-connect",
			cmd:  newCmd{Connect: true},
			root: CLI{Output: "json"},
			want: false,
		},
		{
			name: "non-TTY stdin suppresses auto-connect",
			cmd:  newCmd{Connect: true},
			root: CLI{Output: "text"},
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			io := IO{Stdin: pipeStdin}
			got := shouldAutoConnect(tc.cmd, &tc.root, io)
			if got != tc.want {
				t.Errorf("shouldAutoConnect = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestParseMountFlags(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		specs   []string
		want    []model.Mount
		errSubs string
	}{
		{
			name:  "empty input returns nil",
			specs: nil,
			want:  nil,
		},
		{
			name:  "bind short form",
			specs: []string{"type=bind,src=~/.claude,dst=/home/dev/.claude"},
			want: []model.Mount{
				{Type: "bind", Source: "~/.claude", Target: "/home/dev/.claude"},
			},
		},
		{
			name:  "bind long form + unknown key passthrough",
			specs: []string{"type=bind,source=/opt/data,target=/data,readonly=true"},
			want: []model.Mount{
				{Type: "bind", Source: "/opt/data", Target: "/data", Other: []string{"readonly=true"}},
			},
		},
		{
			name:  "external true",
			specs: []string{"type=volume,source=named,target=/vol,external=true"},
			want: []model.Mount{
				{Type: "volume", Source: "named", Target: "/vol", External: true},
			},
		},
		{
			name:    "missing target rejected",
			specs:   []string{"type=bind,source=/opt"},
			errSubs: "target is required",
		},
		{
			name:    "malformed kv rejected",
			specs:   []string{"type=bind,just-a-word,target=/x"},
			errSubs: "expected key=value pairs",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseMountFlags(tc.specs)
			if tc.errSubs != "" {
				if err == nil || !strings.Contains(err.Error(), tc.errSubs) {
					t.Fatalf("expected error containing %q, got %v", tc.errSubs, err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestBuildNewParamsMountsPropagate(t *testing.T) {
	t.Parallel()
	cmd := newCmd{
		Name:  "k",
		Mount: []string{"type=bind,source=~/.claude,target=/home/dev/.claude"},
	}
	params := buildNewParams(cmd)
	mounts, ok := params["mounts"].([]model.Mount)
	if !ok {
		t.Fatalf("params[\"mounts\"] missing or wrong type: %#v", params["mounts"])
	}
	if len(mounts) != 1 || mounts[0].Target != "/home/dev/.claude" {
		t.Fatalf("unexpected mounts: %+v", mounts)
	}
}

func TestParseGitHubCloneOwnerRepo(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in        string
		wantOwner string
		wantRepo  string
		wantOK    bool
	}{
		{"https://github.com/acme/widget", "acme", "widget", true},
		{"https://github.com/acme/widget.git", "acme", "widget", true},
		{"http://github.com/acme/widget", "acme", "widget", true},
		{"git@github.com:acme/widget.git", "acme", "widget", true},
		{"ssh://git@github.com/acme/widget", "acme", "widget", true},
		{"https://gitlab.com/acme/widget", "", "", false},
		{"https://github.com/acme", "", "", false},
		{"https://github.com/acme/widget/extra", "", "", false},
		{"", "", "", false},
		{"acme/widget", "", "", false}, // pre-shorthand-expansion form
	}
	for _, tc := range cases {
		o, r, ok := parseGitHubCloneOwnerRepo(tc.in)
		if ok != tc.wantOK || o != tc.wantOwner || r != tc.wantRepo {
			t.Errorf("parseGitHubCloneOwnerRepo(%q): got (%q, %q, %v), want (%q, %q, %v)",
				tc.in, o, r, ok, tc.wantOwner, tc.wantRepo, tc.wantOK)
		}
	}
}

func TestValidatePatFlag(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in       string
		wantErr  bool
		wantHint string
	}{
		{in: "", wantErr: false},
		{in: "none", wantErr: false},
		{in: "  none  ", wantErr: false},
		{in: "my-slug", wantErr: false},
		{in: "github_pat_11ABCDE", wantErr: true, wantHint: "drift pat new"},
		{in: "GITHUB_PAT_loud", wantErr: true, wantHint: "drift pat new"},
		{in: "ghp_classicstyle", wantErr: true, wantHint: "drift pat new"},
	}
	for _, tc := range cases {
		err := validatePatFlag(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("validatePatFlag(%q): want error, got nil", tc.in)
				continue
			}
			if !strings.Contains(err.Error(), tc.wantHint) {
				t.Errorf("validatePatFlag(%q): error %q missing hint %q", tc.in, err, tc.wantHint)
			}
		} else if err != nil {
			t.Errorf("validatePatFlag(%q): unexpected error %v", tc.in, err)
		}
	}
}

func TestBuildNewParams_PatSlugPropagates(t *testing.T) {
	t.Parallel()
	got := buildNewParams(newCmd{Name: "k", Pat: "my-slug"})
	if got["pat_slug"] != "my-slug" {
		t.Fatalf("pat_slug = %v, want my-slug", got["pat_slug"])
	}
	got = buildNewParams(newCmd{Name: "k"})
	if _, ok := got["pat_slug"]; ok {
		t.Fatalf("pat_slug should not be set when --pat is empty: %+v", got)
	}
}
