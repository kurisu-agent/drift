package drift

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kurisu-agent/drift/internal/config"
)

func TestMergeSSHArgs(t *testing.T) {
	prev := userHome
	userHome = func() (string, error) { return "/h", nil }
	t.Cleanup(func() { userHome = prev })

	cfg := &config.Client{
		Circuits: map[string]config.ClientCircuit{
			"lab":  {Host: "lab@example", SSHArgs: []string{"-i", "~/keys/lab", "-o", "IdentitiesOnly=yes"}},
			"bare": {Host: "x@y"},
		},
	}

	cases := []struct {
		name    string
		circuit string
		cli     []string
		want    []string
	}{
		{
			name:    "config only, tilde expanded",
			circuit: "lab",
			cli:     nil,
			want:    []string{"-i", "/h/keys/lab", "-o", "IdentitiesOnly=yes"},
		},
		{
			name:    "cli only — config absent",
			circuit: "bare",
			cli:     []string{"-p", "2222"},
			want:    []string{"-p", "2222"},
		},
		{
			name:    "config then cli",
			circuit: "lab",
			cli:     []string{"-p", "2222", "-i", "~/keys/other"},
			want:    []string{"-i", "/h/keys/lab", "-o", "IdentitiesOnly=yes", "-p", "2222", "-i", "/h/keys/other"},
		},
		{
			name:    "unknown circuit collapses to cli only",
			circuit: "ghost",
			cli:     []string{"-F", "~/alt-config"},
			want:    []string{"-F", "/h/alt-config"},
		},
		{
			name:    "both empty returns nil",
			circuit: "bare",
			cli:     nil,
			want:    nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := mergeSSHArgs(cfg, tc.circuit, tc.cli)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("mergeSSHArgs (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExpandSSHTilde(t *testing.T) {
	cases := []struct {
		in, home, want string
	}{
		{"~", "/h", "/h"},
		{"~/", "/h", "/h/"},
		{"~/foo", "/h", "/h/foo"},
		{"~root/foo", "/h", "~root/foo"}, // user-form intentionally unhandled
		{"/abs", "/h", "/abs"},
		{"-i", "/h", "-i"},
		{"~/foo", "", "~/foo"}, // empty home: pass through
	}
	for _, tc := range cases {
		if got := expandSSHTilde(tc.in, tc.home); got != tc.want {
			t.Errorf("expandSSHTilde(%q, %q) = %q, want %q", tc.in, tc.home, got, tc.want)
		}
	}
}

func TestMergeSSHArgs_NilCfg(t *testing.T) {
	prev := userHome
	userHome = func() (string, error) { return "/h", nil }
	t.Cleanup(func() { userHome = prev })

	got := mergeSSHArgs(nil, "whatever", []string{"-p", "22"})
	want := []string{"-p", "22"}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("mergeSSHArgs nil cfg (-want +got):\n%s", diff)
	}
}
