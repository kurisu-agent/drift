package connect_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/internal/connect"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

type fakeServer struct {
	statuses []string // queued responses for successive kart.info calls
	starts   int
	lastArgs map[string]string
}

func (f *fakeServer) call(ctx context.Context, circuit, method string, params, result any) error {
	p, _ := params.(map[string]string)
	f.lastArgs = p
	switch method {
	case wire.MethodKartInfo:
		if len(f.statuses) == 0 {
			return errors.New("unexpected kart.info: queue empty")
		}
		status := f.statuses[0]
		f.statuses = f.statuses[1:]
		buf, _ := json.Marshal(map[string]string{"name": p["name"], "status": status})
		// result is *json.RawMessage — hand back via JSON round-trip.
		raw, ok := result.(*json.RawMessage)
		if !ok {
			return errors.New("fakeServer: unexpected result type")
		}
		*raw = append((*raw)[:0], buf...)
		return nil
	case wire.MethodKartStart:
		f.starts++
		m, ok := result.(*map[string]any)
		if ok {
			*m = map[string]any{"name": p["name"], "status": "running"}
		}
		return nil
	}
	return errors.New("unknown method " + method)
}

type recordedExec struct {
	bin  string
	argv []string
}

func TestRunRunningKartSkipsStart(t *testing.T) {
	f := &fakeServer{statuses: []string{"running"}}
	rec := recordedExec{}
	d := connect.Deps{
		LookPath: func(s string) (string, error) { return "", errors.New("no mosh") },
		Call:     f.call,
		Exec: func(ctx context.Context, bin string, argv []string, stdio connect.Stdio) error {
			rec.bin, rec.argv = bin, argv
			return nil
		},
		Now:   time.Now,
		Sleep: func(time.Duration) {},
	}
	err := connect.Run(context.Background(), d, connect.Options{Circuit: "c", Kart: "k"}, connect.Stdio{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.starts != 0 {
		t.Errorf("starts = %d, want 0 (kart already running)", f.starts)
	}
	if rec.bin != "ssh" {
		t.Errorf("bin = %q, want ssh (no mosh)", rec.bin)
	}
	wantArgv := []string{"-t", "drift.c", "devpod", "ssh", "k"}
	if !equal(rec.argv, wantArgv) {
		t.Errorf("argv = %v, want %v", rec.argv, wantArgv)
	}
}

func TestRunStoppedKartTriggersStart(t *testing.T) {
	f := &fakeServer{statuses: []string{"stopped", "running"}}
	rec := recordedExec{}
	d := connect.Deps{
		LookPath: func(s string) (string, error) { return "/usr/bin/mosh", nil },
		Call:     f.call,
		Exec: func(ctx context.Context, bin string, argv []string, stdio connect.Stdio) error {
			rec.bin, rec.argv = bin, argv
			return nil
		},
		Now:   time.Now,
		Sleep: func(time.Duration) {},
	}
	err := connect.Run(context.Background(), d, connect.Options{Circuit: "c", Kart: "k"}, connect.Stdio{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if f.starts != 1 {
		t.Errorf("starts = %d, want 1", f.starts)
	}
	if rec.bin != "mosh" {
		t.Errorf("bin = %q, want mosh", rec.bin)
	}
	wantArgv := []string{"drift.c", "--", "devpod", "ssh", "k"}
	if !equal(rec.argv, wantArgv) {
		t.Errorf("argv = %v, want %v", rec.argv, wantArgv)
	}
}

func TestRunForwardAgentAddsDashA(t *testing.T) {
	f := &fakeServer{statuses: []string{"running"}}
	rec := recordedExec{}
	d := connect.Deps{
		LookPath: func(s string) (string, error) { return "/usr/bin/mosh", nil },
		Call:     f.call,
		Exec: func(ctx context.Context, bin string, argv []string, stdio connect.Stdio) error {
			rec.bin, rec.argv = bin, argv
			return nil
		},
		Now:   time.Now,
		Sleep: func(time.Duration) {},
	}
	err := connect.Run(context.Background(), d, connect.Options{
		Circuit: "c", Kart: "k", ForceSSH: true, ForwardAgent: true,
	}, connect.Stdio{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.bin != "ssh" {
		t.Errorf("bin = %q, want ssh (ForceSSH)", rec.bin)
	}
	wantArgv := []string{"-t", "-A", "drift.c", "devpod", "ssh", "k"}
	if !equal(rec.argv, wantArgv) {
		t.Errorf("argv = %v, want %v", rec.argv, wantArgv)
	}
}

func TestRunStaleKartSurfacesConflict(t *testing.T) {
	f := &fakeServer{statuses: []string{"stale_kart"}}
	d := connect.Deps{
		LookPath: func(s string) (string, error) { return "", errors.New("") },
		Call:     f.call,
		Exec:     func(context.Context, string, []string, connect.Stdio) error { t.Fatal("exec must not run"); return nil },
		Now:      time.Now,
		Sleep:    func(time.Duration) {},
	}
	err := connect.Run(context.Background(), d, connect.Options{Circuit: "c", Kart: "k"}, connect.Stdio{})
	if err == nil {
		t.Fatal("Run returned nil err on stale_kart")
	}
	var re *rpcerr.Error
	if !errors.As(err, &re) {
		t.Fatalf("err is not *rpcerr.Error: %T", err)
	}
	if re.Code != rpcerr.CodeConflict {
		t.Errorf("code = %d, want %d", re.Code, rpcerr.CodeConflict)
	}
}

func TestRunAutostartTimeout(t *testing.T) {
	// Repeatedly return busy — poll should time out.
	f := &fakeServer{}
	for i := 0; i < 50; i++ {
		f.statuses = append(f.statuses, "stopped")
	}
	// Fake clock: starts at t0; each Now() advances by 10s, so the 30s
	// default timeout trips after 3 calls.
	var now time.Time
	d := connect.Deps{
		LookPath: func(s string) (string, error) { return "", errors.New("") },
		Call:     f.call,
		Exec:     func(context.Context, string, []string, connect.Stdio) error { t.Fatal("exec must not run"); return nil },
		Now: func() time.Time {
			now = now.Add(10 * time.Second)
			return now
		},
		Sleep: func(time.Duration) {},
	}
	err := connect.Run(context.Background(), d, connect.Options{Circuit: "c", Kart: "k"}, connect.Stdio{})
	if err == nil {
		t.Fatal("Run returned nil err on timeout")
	}
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != "kart_autostart_timeout" {
		t.Fatalf("want type=kart_autostart_timeout, got %v", err)
	}
}

func TestTransport(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		force    bool
		lookPath func(string) (string, error)
		want     string
	}{
		{"mosh present", false, func(string) (string, error) { return "/usr/bin/mosh", nil }, "mosh"},
		{"mosh missing", false, func(string) (string, error) { return "", errors.New("not found") }, "ssh"},
		{"force ssh skips lookup", true, func(string) (string, error) {
			t.Error("LookPath called despite ForceSSH")
			return "/usr/bin/mosh", nil
		}, "ssh"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := connect.Transport(tc.lookPath, tc.force); got != tc.want {
				t.Errorf("Transport = %q, want %q", got, tc.want)
			}
		})
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
