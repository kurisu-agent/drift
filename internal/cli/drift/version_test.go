package drift

import (
	"testing"

	"github.com/kurisu-agent/drift/internal/version"
)

// TestFormatVersionText locks in the `drift <tag> (<short-commit>)` shape:
// release builds carry a tag AND a commit, nix dev builds carry "dev" +
// commit, and legacy builds that stuffed the hash into Version alone
// must not render `drift abc1234 (abc1234)`.
func TestFormatVersionText(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		info version.Info
		want string
	}{
		{
			name: "tag plus full commit",
			info: version.Info{Version: "v0.2.0", Commit: "caa63a8c2ff1abcdef"},
			want: "drift v0.2.0 (caa63a8)",
		},
		{
			name: "dev plus short commit",
			info: version.Info{Version: "dev", Commit: "caa63a8"},
			want: "drift dev (caa63a8)",
		},
		{
			name: "commit absent",
			info: version.Info{Version: "v0.2.0"},
			want: "drift v0.2.0",
		},
		{
			name: "legacy: commit already embedded in Version",
			info: version.Info{Version: "caa63a8", Commit: "caa63a8"},
			want: "drift caa63a8",
		},
		{
			name: "dirty placeholder passes through",
			info: version.Info{Version: "dev", Commit: "dirty"},
			want: "drift dev (dirty)",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := formatVersionText(tc.info)
			if got != tc.want {
				t.Errorf("formatVersionText = %q, want %q", got, tc.want)
			}
		})
	}
}
