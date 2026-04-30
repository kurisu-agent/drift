package drift

import (
	"testing"
)

func TestNameFromRepoURL(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"https://github.com/acme/widget", "widget"},
		{"https://github.com/acme/widget.git", "widget"},
		{"https://github.com/acme/widget/", "widget"},
		{"git@github.com:acme/widget.git", "widget"},
		{"ssh://git@github.com/acme/Widget", "widget"},
		{"https://example.com/some/deep/path/My_Repo.Thing", "my-repo-thing"},
		// Names that fail kart validator (start with digit, all symbols) come back empty.
		{"https://github.com/acme/9livesofapp", ""},
		{"https://github.com/acme/---", ""},
	}
	for _, tc := range cases {
		got := nameFromRepoURL(tc.in)
		if got != tc.want {
			t.Errorf("nameFromRepoURL(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSuggestKartName(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cmd  newCmd
		want string
	}{
		{name: "empty", cmd: newCmd{}, want: ""},
		{name: "from clone", cmd: newCmd{Clone: "https://github.com/acme/widget.git"}, want: "widget"},
		{name: "from starter when clone empty", cmd: newCmd{Starter: "https://github.com/acme/template"}, want: "template"},
		{name: "clone wins over starter", cmd: newCmd{Clone: "https://github.com/acme/c", Starter: "https://github.com/acme/s"}, want: "c"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := suggestKartName(&tc.cmd)
			if got != tc.want {
				t.Errorf("suggestKartName = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestInitialSourceMode(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		cmd  newCmd
		want kartSourceMode
	}{
		{name: "default none", cmd: newCmd{}, want: sourceNone},
		{name: "clone wins", cmd: newCmd{Clone: "x"}, want: sourceClone},
		{name: "starter when no clone", cmd: newCmd{Starter: "x"}, want: sourceStarter},
		{name: "clone over starter", cmd: newCmd{Clone: "c", Starter: "s"}, want: sourceClone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := initialSourceMode(&tc.cmd)
			if got != tc.want {
				t.Errorf("initialSourceMode = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestKartNameRegex(t *testing.T) {
	t.Parallel()
	good := []string{"a", "kart", "my-kart", "kart1", "a-1-2"}
	bad := []string{"", "1kart", "-kart", "Kart", "my_kart", "my.kart", "k!"}
	for _, s := range good {
		if !kartNameRe.MatchString(s) {
			t.Errorf("kartNameRe rejected %q, want match", s)
		}
	}
	for _, s := range bad {
		if kartNameRe.MatchString(s) {
			t.Errorf("kartNameRe accepted %q, want no match", s)
		}
	}
}
