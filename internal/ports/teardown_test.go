package ports

import (
	"context"
	"reflect"
	"testing"
)

func TestTeardownKart_aliveMasterCancelsAndStops(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	host := SSHHost("alpha", "web")
	d := newFakeDriver()
	d.alive[host] = true
	d.installed[host] = []livePair{{Local: 3000, Remote: 3000}, {Local: 5433, Remote: 5432}}

	livePathStr, err := livePath()
	if err != nil {
		t.Fatalf("livePath: %v", err)
	}
	if err := saveLiveCache(livePathStr, &liveCache{Hosts: map[string]liveHost{
		host: {Forwards: d.installed[host]},
	}}); err != nil {
		t.Fatalf("seed cache: %v", err)
	}

	state := &State{}
	if _, err := AddForward(state, allFreeProber(), "alpha", "web", 3000, 0, SourceExplicit); err != nil {
		t.Fatalf("seed state: %v", err)
	}

	if err := TeardownKart(context.Background(), d, state, "alpha", "web"); err != nil {
		t.Fatalf("TeardownKart: %v", err)
	}

	wantCalls := []string{
		"check " + host,
		fwdLog("cancel", host, 3000, 3000),
		fwdLog("cancel", host, 5433, 5432),
		"stop " + host,
	}
	if !reflect.DeepEqual(d.calls, wantCalls) {
		t.Errorf("calls = %v, want %v", d.calls, wantCalls)
	}

	// State.yaml view of forwards is unchanged — teardown only touches
	// the live ssh world.
	if got := state.Get("alpha", "web"); len(got) != 1 || got[0].Remote != 3000 {
		t.Errorf("state.yaml entries should be untouched, got %+v", got)
	}

	// Live cache row is cleared so a later reconcile starts fresh.
	live, err := loadLiveCache(livePathStr)
	if err != nil {
		t.Fatalf("loadLiveCache: %v", err)
	}
	if pairs := live.get(host); len(pairs) != 0 {
		t.Errorf("cache row should be empty, got %v", pairs)
	}
}

func TestTeardownKart_deadMasterJustClearsCache(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())

	host := SSHHost("alpha", "web")
	d := newFakeDriver()
	// alive[host] = false (default zero-value): master is gone.

	livePathStr, _ := livePath()
	_ = saveLiveCache(livePathStr, &liveCache{Hosts: map[string]liveHost{
		host: {Forwards: []livePair{{Local: 3000, Remote: 3000}}},
	}})

	if err := TeardownKart(context.Background(), d, &State{}, "alpha", "web"); err != nil {
		t.Fatalf("TeardownKart: %v", err)
	}

	// Should call Check, then nothing else — no master to stop, no
	// forwards to cancel against a dead master.
	wantCalls := []string{"check " + host}
	if !reflect.DeepEqual(d.calls, wantCalls) {
		t.Errorf("calls = %v, want %v (no cancel/stop on dead master)", d.calls, wantCalls)
	}

	live, _ := loadLiveCache(livePathStr)
	if pairs := live.get(host); len(pairs) != 0 {
		t.Errorf("dead-master path should clear cache row, got %v", pairs)
	}
}
