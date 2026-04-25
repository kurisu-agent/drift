package ports

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestStateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "ports.yaml")

	in := &State{
		Forwards: map[string][]Forward{
			KartKey("alpha", "web"): {
				{Local: 3000, Remote: 3000, Source: SourceExplicit},
				{Local: 5433, Remote: 5432, RemappedFrom: 5432, Source: SourceExplicit},
			},
			KartKey("beta", "scratch"): {
				{Local: 9229, Remote: 9229, Source: SourceAuto},
			},
		},
	}

	if err := Save(path, in); err != nil {
		t.Fatalf("Save: %v", err)
	}

	out, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if out.Version != CurrentVersion {
		t.Errorf("Version = %d, want %d", out.Version, CurrentVersion)
	}
	if !reflect.DeepEqual(in.Forwards, out.Forwards) {
		t.Errorf("Forwards mismatch:\n got  %+v\n want %+v", out.Forwards, in.Forwards)
	}
}

func TestLoadMissing(t *testing.T) {
	t.Parallel()
	s, err := Load(filepath.Join(t.TempDir(), "nope.yaml"))
	if err != nil {
		t.Fatalf("Load on missing: %v", err)
	}
	if s.Version != CurrentVersion {
		t.Errorf("Version = %d, want %d", s.Version, CurrentVersion)
	}
	if len(s.Forwards) != 0 {
		t.Errorf("Forwards should be empty, got %v", s.Forwards)
	}
}

func TestStateValidate(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		state  State
		wantOK bool
	}{
		{
			name: "valid",
			state: State{Forwards: map[string][]Forward{
				"alpha/web": {{Local: 3000, Remote: 3000}},
			}},
			wantOK: true,
		},
		{
			name: "bad key (no slash)",
			state: State{Forwards: map[string][]Forward{
				"alphaweb": {{Local: 3000, Remote: 3000}},
			}},
		},
		{
			name: "local out of range",
			state: State{Forwards: map[string][]Forward{
				"alpha/web": {{Local: 0, Remote: 3000}},
			}},
		},
		{
			name: "duplicate local",
			state: State{Forwards: map[string][]Forward{
				"alpha/web": {
					{Local: 3000, Remote: 3000},
					{Local: 3000, Remote: 3001},
				},
			}},
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			err := c.state.Validate()
			if c.wantOK && err != nil {
				t.Errorf("want ok, got %v", err)
			}
			if !c.wantOK && err == nil {
				t.Errorf("want error, got nil")
			}
		})
	}
}

func TestSplitKartKey(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in     string
		c, k   string
		wantOK bool
	}{
		{"alpha/web", "alpha", "web", true},
		{"alpha/", "", "", false},
		{"/web", "", "", false},
		{"alpha/web/extra", "", "", false},
		{"alphaweb", "", "", false},
	}
	for _, tc := range cases {
		c, k, ok := SplitKartKey(tc.in)
		if ok != tc.wantOK || c != tc.c || k != tc.k {
			t.Errorf("SplitKartKey(%q) = (%q, %q, %v), want (%q, %q, %v)", tc.in, c, k, ok, tc.c, tc.k, tc.wantOK)
		}
	}
}
