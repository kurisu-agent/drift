package connect_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/internal/connect"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// serverArgv is what a modern lakitu returns from kart.connect: the
// actual remote-command token stream including a DEVPOD_HOME env prefix,
// an absolute devpod path, and the pinned --agent-forwarding=false flag.
// The client splices this into the ssh argv verbatim, and wraps it in a
// `script -qfc …` shell token on the mosh argv.
var serverArgv = []string{
	"env", "DEVPOD_HOME=/h/.drift/devpod",
	"/bin/devpod", "ssh", "k",
	"--agent-forwarding=false",
}

// moshLocalePrefix mirrors the `env -u …` prefix buildConnectArgv emits
// on the mosh path to strip locale forwarding. Kept in lockstep with
// connect.go's moshLocaleStrip + moshLocaleForce — if those lists
// change, this does too.
var moshLocalePrefix = []string{
	"-u", "LANGUAGE",
	"-u", "LC_CTYPE", "-u", "LC_NUMERIC", "-u", "LC_TIME",
	"-u", "LC_COLLATE", "-u", "LC_MONETARY", "-u", "LC_MESSAGES",
	"-u", "LC_PAPER", "-u", "LC_NAME", "-u", "LC_ADDRESS",
	"-u", "LC_TELEPHONE", "-u", "LC_MEASUREMENT", "-u", "LC_IDENTIFICATION",
	"LANG=C.UTF-8", "LC_ALL=C.UTF-8",
}

// moshExpected assembles the argv the mosh path produces: env-unset
// prefix + mosh-args + the remote wrapped in `script -qfc '<quoted>'
// /dev/null`. Tests pass the mosh-specific middle (e.g. `drift.c --`)
// and the serverArgv-equivalent remote.
func moshExpected(moshMid []string, remote []string) []string {
	out := make([]string, 0, len(moshLocalePrefix)+1+len(moshMid)+4)
	out = append(out, moshLocalePrefix...)
	out = append(out, "mosh")
	out = append(out, moshMid...)
	out = append(out, "script", "-qfc", connectShellQuote(remote), "/dev/null")
	return out
}

// connectShellQuote mirrors connect.posixQuote / shellQuoteArgs — the
// tests can't reach the unexported helper, so reimplement it here with
// the same escaping rules.
func connectShellQuote(args []string) string {
	parts := make([]string, 0, len(args))
	for _, a := range args {
		parts = append(parts, "'"+strings.ReplaceAll(a, "'", `'\''`)+"'")
	}
	return strings.Join(parts, " ")
}

type fakeServer struct {
	statuses    []string // queued responses for successive kart.info calls
	starts      int
	lastArgs    map[string]string
	connectFail bool // when true, kart.connect returns method_not_found
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
		raw, ok := result.(*json.RawMessage)
		if !ok {
			return errors.New("fakeServer: unexpected result type for kart.info")
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
	case wire.MethodKartConnect:
		if f.connectFail {
			return rpcerr.New(rpcerr.CodeUserError, "method_not_found",
				"method %q not implemented", method)
		}
		res, ok := result.(*wire.KartConnectResult)
		if !ok {
			return errors.New("fakeServer: unexpected result type for kart.connect")
		}
		res.Argv = append(res.Argv[:0], serverArgv...)
		return nil
	case wire.MethodKartSessionEnv:
		// Only reached by the legacy fallback path. Leave env empty.
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
	// Server-resolved argv lives between `drift.c` and end of slice.
	wantArgv := append([]string{"-t", "drift.c"}, serverArgv...)
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
	if rec.bin != "env" {
		t.Errorf("bin = %q, want env (mosh invocation is locale-stripped)", rec.bin)
	}
	wantArgv := moshExpected([]string{"drift.c", "--"}, serverArgv)
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
	wantArgv := append([]string{"-t", "-A", "drift.c"}, serverArgv...)
	if !equal(rec.argv, wantArgv) {
		t.Errorf("argv = %v, want %v", rec.argv, wantArgv)
	}
}

// TestRunSSHArgsSpliceOnSSHPath covers the ssh transport: args slot in
// between `-A` and the target host so they apply to the connection, not
// the remote command.
func TestRunSSHArgsSpliceOnSSHPath(t *testing.T) {
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
	err := connect.Run(context.Background(), d, connect.Options{
		Circuit:      "c",
		Kart:         "k",
		ForwardAgent: true,
		SSHArgs:      []string{"-i", "/k/id", "-o", "IdentitiesOnly=yes"},
	}, connect.Stdio{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.bin != "ssh" {
		t.Errorf("bin = %q, want ssh (mosh absent)", rec.bin)
	}
	wantArgv := append(
		[]string{"-t", "-A", "-i", "/k/id", "-o", "IdentitiesOnly=yes", "drift.c"},
		serverArgv...,
	)
	if !equal(rec.argv, wantArgv) {
		t.Errorf("argv = %v, want %v", rec.argv, wantArgv)
	}
}

// TestRunSSHArgsForwardsThroughMosh covers the mosh transport: mosh uses
// ssh to bootstrap mosh-server on the remote end, and --ssh="ssh …" swaps
// in our flag-ladened ssh for that bootstrap. Each arg is POSIX-single-
// quoted so paths with spaces / embedded quotes survive mosh's shell
// split.
func TestRunSSHArgsForwardsThroughMosh(t *testing.T) {
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
		Circuit: "c",
		Kart:    "k",
		SSHArgs: []string{"-i", "/k/has space/id", "-o", "Foo='bar'"},
	}, connect.Stdio{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if rec.bin != "env" {
		t.Errorf("bin = %q, want env (mosh invocation is locale-stripped)", rec.bin)
	}
	wantOverride := `--ssh=ssh '-i' '/k/has space/id' '-o' 'Foo='\''bar'\'''`
	wantArgv := moshExpected([]string{wantOverride, "drift.c", "--"}, serverArgv)
	if !equal(rec.argv, wantArgv) {
		t.Errorf("argv = %v\n want %v", rec.argv, wantArgv)
	}
}

// TestRunFallsBackWhenKartConnectMissing covers the cross-version case:
// newer drift client talking to a lakitu that predates kart.connect. The
// RPC returns method_not_found and the client synthesizes the historic
// `[devpod, ssh, <kart>]` argv locally (no env prefix, no devpod abs
// path — that's a post-upgrade affordance only). The user can still
// connect, just without the server-managed isolation.
func TestRunFallsBackWhenKartConnectMissing(t *testing.T) {
	f := &fakeServer{
		statuses:    []string{"running"},
		connectFail: true,
	}
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
	wantArgv := []string{"-t", "drift.c", "devpod", "ssh", "k"}
	if !equal(rec.argv, wantArgv) {
		t.Errorf("fallback argv = %v, want %v", rec.argv, wantArgv)
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

// AfterExec must fire after Exec returns regardless of exit code, so
// teardown can run even when the user's shell exited non-zero or Exec
// itself errored. The plan-15 ports teardown depends on this.
func TestRunAfterExecFires(t *testing.T) {
	cases := []struct {
		name    string
		execErr error
	}{
		{name: "success", execErr: nil},
		{name: "shell error", execErr: errors.New("rc=1")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := &fakeServer{statuses: []string{"running"}}
			afterFired := false
			d := connect.Deps{
				LookPath: func(s string) (string, error) { return "", errors.New("no mosh") },
				Call:     f.call,
				Exec: func(context.Context, string, []string, connect.Stdio) error {
					return c.execErr
				},
				AfterExec: func(context.Context) { afterFired = true },
				Now:       time.Now,
				Sleep:     func(time.Duration) {},
			}
			_ = connect.Run(context.Background(), d, connect.Options{Circuit: "c", Kart: "k"}, connect.Stdio{})
			if !afterFired {
				t.Errorf("AfterExec did not fire (%s case)", c.name)
			}
		})
	}
}

// AfterExec must NOT fire when Run aborts before reaching Exec — e.g.
// the kart.connect RPC errors out, or BeforeExec returns non-nil.
// Otherwise teardown would run on connects that never happened.
func TestRunAfterExecSkippedOnPreExecError(t *testing.T) {
	f := &fakeServer{statuses: []string{"stale_kart"}}
	afterFired := false
	d := connect.Deps{
		LookPath:  func(s string) (string, error) { return "", errors.New("") },
		Call:      f.call,
		Exec:      func(context.Context, string, []string, connect.Stdio) error { return nil },
		AfterExec: func(context.Context) { afterFired = true },
		Now:       time.Now,
		Sleep:     func(time.Duration) {},
	}
	_ = connect.Run(context.Background(), d, connect.Options{Circuit: "c", Kart: "k"}, connect.Stdio{})
	if afterFired {
		t.Errorf("AfterExec fired even though Run aborted before Exec")
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
