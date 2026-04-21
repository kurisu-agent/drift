package drift

import (
	"bytes"
	"testing"
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
