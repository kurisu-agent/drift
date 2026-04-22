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
