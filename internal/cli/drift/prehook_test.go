package drift

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/internal/cli/style"
)

func TestClientState_RoundTrip(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	want := clientState{
		LastUpdateCheck: time.Date(2026, 4, 23, 12, 0, 0, 0, time.UTC),
		LatestVersion:   "0.3.1",
	}
	if err := saveClientState(want); err != nil {
		t.Fatalf("save: %v", err)
	}
	got := loadClientState()
	if got.LatestVersion != want.LatestVersion {
		t.Errorf("LatestVersion: got %q, want %q", got.LatestVersion, want.LatestVersion)
	}
	if !got.LastUpdateCheck.Equal(want.LastUpdateCheck) {
		t.Errorf("LastUpdateCheck: got %v, want %v", got.LastUpdateCheck, want.LastUpdateCheck)
	}
}

func TestLoadClientState_MissingFileIsZeroValue(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	got := loadClientState()
	if !got.LastUpdateCheck.IsZero() || got.LatestVersion != "" {
		t.Errorf("missing file: got %+v, want zero-value", got)
	}
}

func TestLoadClientState_MalformedJSONIsZeroValue(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if err := os.MkdirAll(filepath.Join(dir, "drift"), 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "drift", "state.json"), []byte("{not valid"), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got := loadClientState()
	if !got.LastUpdateCheck.IsZero() || got.LatestVersion != "" {
		t.Errorf("malformed JSON: got %+v, want zero-value", got)
	}
}

func TestSaveClientState_PrettyPrintsJSON(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)

	if err := saveClientState(clientState{LatestVersion: "1.2.3"}); err != nil {
		t.Fatalf("save: %v", err)
	}
	data, err := os.ReadFile(filepath.Join(dir, "drift", "state.json"))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if !strings.Contains(string(data), "\n") {
		t.Errorf("state.json should be pretty-printed; got %q", data)
	}
	var got clientState
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.LatestVersion != "1.2.3" {
		t.Errorf("LatestVersion: got %q, want %q", got.LatestVersion, "1.2.3")
	}
}

func TestUpdateBannerLine(t *testing.T) {
	p := style.For(&bytes.Buffer{}, false) // NO_COLOR-unaware buffer → plain text

	cases := []struct {
		name, cur, latest string
		wantEmpty         bool
	}{
		{"new version available", "0.2.0", "0.3.0", false},
		{"v-prefix on both is trimmed", "v0.2.0", "v0.3.0", false},
		{"v-prefix on one side is trimmed", "0.2.0", "v0.3.0", false},
		{"same version suppresses", "0.3.0", "0.3.0", true},
		{"devel suppresses", "devel", "0.3.0", true},
		{"empty current suppresses", "", "0.3.0", true},
		{"empty latest suppresses", "0.2.0", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := updateBannerLine(tc.cur, tc.latest, p)
			if tc.wantEmpty && got != "" {
				t.Errorf("expected no banner, got %q", got)
			}
			if !tc.wantEmpty && got == "" {
				t.Errorf("expected a banner, got empty")
			}
			if !tc.wantEmpty && !strings.Contains(got, "drift update") {
				t.Errorf("banner should mention `drift update`, got %q", got)
			}
		})
	}
}

func TestUpdateCheckEnabled(t *testing.T) {
	prev := isTTYFn
	t.Cleanup(func() { isTTYFn = prev })
	tty := func(bool) {}
	tty(true)

	cases := []struct {
		name    string
		cli     CLI
		cmd     string
		skipEnv string
		tty     bool
		want    bool
	}{
		{"default command ok", CLI{Output: "text"}, "status", "", true, true},
		{"json output off", CLI{Output: "json"}, "status", "", true, false},
		{"DRIFT_SKIP_UPDATE_CHECK off", CLI{Output: "text"}, "status", "1", true, false},
		{"non-tty stderr off", CLI{Output: "text"}, "status", "", false, false},
		{"help off", CLI{Output: "text"}, "help", "", true, false},
		{"update off", CLI{Output: "text"}, "update", "", true, false},
		{"ssh-proxy off", CLI{Output: "text"}, "ssh-proxy <alias>", "", true, false},
		{"empty command off", CLI{Output: "text"}, "", "", true, false},
		{"multi-word command ok", CLI{Output: "text"}, "connect <name>", "", true, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DRIFT_SKIP_UPDATE_CHECK", tc.skipEnv)
			isTTYFn = func(any) bool { return tc.tty }
			got := updateCheckEnabled(&tc.cli, IO{Stderr: os.Stderr}, tc.cmd)
			if got != tc.want {
				t.Errorf("updateCheckEnabled(%q, tty=%v) = %v, want %v", tc.cmd, tc.tty, got, tc.want)
			}
		})
	}
}

func TestScheduleUpdateCheck_SkipsWhenRecent(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	if err := saveClientState(clientState{LastUpdateCheck: time.Now().Add(-1 * time.Hour)}); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	var called atomic.Int32
	prev := fetchLatestReleaseFn
	t.Cleanup(func() { fetchLatestReleaseFn = prev })
	fetchLatestReleaseFn = func(context.Context, string, string) (*ghRelease, error) {
		called.Add(1)
		return &ghRelease{TagName: "v9.9.9"}, nil
	}

	scheduleUpdateCheck()
	time.Sleep(50 * time.Millisecond) // give any stray goroutine time to misbehave
	if called.Load() != 0 {
		t.Errorf("recent check should skip GitHub call; fired %d times", called.Load())
	}
}

func TestScheduleUpdateCheck_FiresWhenStale(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	// No state file → LastUpdateCheck is zero → must fire.
	done := make(chan struct{})
	prev := fetchLatestReleaseFn
	t.Cleanup(func() { fetchLatestReleaseFn = prev })
	fetchLatestReleaseFn = func(context.Context, string, string) (*ghRelease, error) {
		defer close(done)
		return &ghRelease{TagName: "v1.2.3"}, nil
	}

	scheduleUpdateCheck()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("background check did not fire within 2s")
	}
	// Allow saveClientState to finish after the return from fetchLatestReleaseFn.
	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		st := loadClientState()
		if st.LatestVersion == "1.2.3" && !st.LastUpdateCheck.IsZero() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	st := loadClientState()
	t.Errorf("state not persisted: %+v", st)
}

func TestBackgroundUpdateCheck_SilentOnError(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	prev := fetchLatestReleaseFn
	t.Cleanup(func() { fetchLatestReleaseFn = prev })
	fetchLatestReleaseFn = func(context.Context, string, string) (*ghRelease, error) {
		return nil, errors.New("network down")
	}

	backgroundUpdateCheck()

	st := loadClientState()
	if !st.LastUpdateCheck.IsZero() || st.LatestVersion != "" {
		t.Errorf("failed fetch must not touch state.json; got %+v", st)
	}
}
