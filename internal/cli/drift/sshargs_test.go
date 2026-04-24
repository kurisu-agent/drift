package drift

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestExpandCLISSHArgs(t *testing.T) {
	prev := userHome
	userHome = func() (string, error) { return "/h", nil }
	t.Cleanup(func() { userHome = prev })

	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{name: "nil → nil", in: nil, want: nil},
		{name: "empty → nil", in: []string{}, want: nil},
		{
			name: "tilde expanded on path args",
			in:   []string{"-i", "~/keys/lab", "-o", "IdentitiesOnly=yes"},
			want: []string{"-i", "/h/keys/lab", "-o", "IdentitiesOnly=yes"},
		},
		{
			name: "bare tilde → home",
			in:   []string{"-F", "~"},
			want: []string{"-F", "/h"},
		},
		{
			name: "non-tilde passes through",
			in:   []string{"-p", "2222"},
			want: []string{"-p", "2222"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := expandCLISSHArgs(tc.in)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("expandCLISSHArgs (-want +got):\n%s", diff)
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
