package server

import (
	"os"
	"path/filepath"
	"testing"
)

func TestBrowseStateRoundTrip(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "filebrowser.json")
	want := browseState{PID: 4242, Port: 31337, Root: "/some/root"}
	if err := writeBrowseState(path, want); err != nil {
		t.Fatalf("writeBrowseState: %v", err)
	}
	got, ok := readBrowseState(path)
	if !ok {
		t.Fatal("readBrowseState reported not-ok")
	}
	if got != want {
		t.Errorf("round-trip mismatch: got %+v, want %+v", got, want)
	}
}

func TestReadBrowseStateMissing(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if _, ok := readBrowseState(filepath.Join(dir, "missing.json")); ok {
		t.Error("readBrowseState should return ok=false for missing file")
	}
}

func TestReadBrowseStateRejectsBogusPID(t *testing.T) {
	t.Parallel()
	// PID=0 / port=0 are sentinels for "stale or torn-write". The spawn
	// path captures a real pid before Release; anything else is a sign
	// the file got corrupted and the safer default is to re-spawn.
	dir := t.TempDir()
	cases := []string{
		`{"pid":0,"port":31337,"root":"/x"}`,
		`{"pid":-1,"port":31337,"root":"/x"}`,
		`{"pid":42,"port":0,"root":"/x"}`,
		`not json at all`,
	}
	for i, body := range cases {
		path := filepath.Join(dir, "case.json")
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatalf("case %d write: %v", i, err)
		}
		if _, ok := readBrowseState(path); ok {
			t.Errorf("case %d: expected ok=false for %q", i, body)
		}
	}
}

func TestProcessAliveZeroIsDead(t *testing.T) {
	t.Parallel()
	if processAlive(0) {
		t.Error("PID 0 should be reported dead")
	}
	if processAlive(-1) {
		t.Error("PID -1 should be reported dead")
	}
}
