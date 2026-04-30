package version_test

import (
	"testing"

	"github.com/kurisu-agent/drift/internal/version"
)

func TestGet_alwaysReturnsVersion(t *testing.T) {
	info := version.Get()
	if info.Version == "" {
		t.Error("Version must not be empty — should fall back to 'devel'")
	}
	if info.APISchema < 1 {
		t.Errorf("APISchema = %d, want >= 1", info.APISchema)
	}
}

// TestFormat locks in the `<binary> <tag> (<short-commit>)` shape
// shared by drift and lakitu. Release builds carry a tag AND a commit,
// nix dev builds carry "dev" + commit, and legacy builds that stuffed
// the hash into Version alone must not render as e.g. `drift abc1234
// (abc1234)`.
func TestFormat(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		binary string
		info   version.Info
		want   string
	}{
		{
			name:   "tag plus full commit",
			binary: "drift",
			info:   version.Info{Version: "v0.2.0", Commit: "caa63a8c2ff1abcdef"},
			want:   "drift v0.2.0 (caa63a8)",
		},
		{
			name:   "dev plus short commit",
			binary: "lakitu",
			info:   version.Info{Version: "dev", Commit: "caa63a8"},
			want:   "lakitu dev (caa63a8)",
		},
		{
			name:   "commit absent",
			binary: "drift",
			info:   version.Info{Version: "v0.2.0"},
			want:   "drift v0.2.0",
		},
		{
			name:   "legacy: commit already embedded in Version",
			binary: "drift",
			info:   version.Info{Version: "caa63a8", Commit: "caa63a8"},
			want:   "drift caa63a8",
		},
		{
			name:   "dirty placeholder passes through",
			binary: "lakitu",
			info:   version.Info{Version: "dev", Commit: "dirty"},
			want:   "lakitu dev (dirty)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := tc.info.Format(tc.binary)
			if got != tc.want {
				t.Errorf("Format = %q, want %q", got, tc.want)
			}
		})
	}
}
