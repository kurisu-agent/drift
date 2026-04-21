package client

import (
	"testing"
)

// TestRemoteArgv_RespectsEnvSetAfterPackageInit is the regression guard
// for the `drift --debug` streaming bug. The original code captured
// DRIFT_DEBUG in a package-level var so env set by the drift CLI AFTER
// its own init (Kong's `default:"true"` setter runs in Run(), not init)
// was invisible to this package's transport. If the check is back on
// a package-level capture, this test fails because the env below is
// set after the package init.
func TestRemoteArgv_RespectsEnvSetAfterPackageInit(t *testing.T) {
	// off
	t.Setenv("DRIFT_DEBUG", "")
	got := remoteArgv()
	if len(got) == 2 && got[0] == "lakitu" && got[1] == "rpc" {
		// good
	} else {
		t.Errorf("off: remoteArgv()=%v, want [lakitu rpc]", got)
	}

	// on
	t.Setenv("DRIFT_DEBUG", "1")
	got = remoteArgv()
	want := []string{"env", "LAKITU_DEBUG=1", "lakitu", "rpc"}
	if len(got) != len(want) {
		t.Fatalf("on: remoteArgv()=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("on: remoteArgv()[%d]=%q, want %q", i, got[i], want[i])
		}
	}
}

func TestDebugEnabled_ReadsFreshEachCall(t *testing.T) {
	t.Setenv("DRIFT_DEBUG", "")
	if debugEnabled() {
		t.Errorf("want false when env unset")
	}
	t.Setenv("DRIFT_DEBUG", "1")
	if !debugEnabled() {
		t.Errorf("want true after set")
	}
	t.Setenv("DRIFT_DEBUG", "")
	if debugEnabled() {
		t.Errorf("want false after unset")
	}
}
