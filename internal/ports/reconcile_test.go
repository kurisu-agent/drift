package ports

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"testing"
)

// fakeDriver records every call and reports configurable Check results.
type fakeDriver struct {
	alive map[string]bool
	// installed is the simulated set of forwards the master "has" right
	// now. The reconcile cache tracks what *we* think is installed; this
	// is the actual master state for assertion purposes.
	installed map[string][]livePair
	calls     []string
	addErr    error
	startErr  error
}

func newFakeDriver() *fakeDriver {
	return &fakeDriver{
		alive:     map[string]bool{},
		installed: map[string][]livePair{},
	}
}

func (f *fakeDriver) Check(_ context.Context, host string) (bool, error) {
	f.calls = append(f.calls, "check "+host)
	return f.alive[host], nil
}

func (f *fakeDriver) StartMaster(_ context.Context, host string) error {
	f.calls = append(f.calls, "start "+host)
	if f.startErr != nil {
		return f.startErr
	}
	f.alive[host] = true
	f.installed[host] = nil
	return nil
}

func (f *fakeDriver) StopMaster(_ context.Context, host string) error {
	f.calls = append(f.calls, "stop "+host)
	f.alive[host] = false
	delete(f.installed, host)
	return nil
}

func (f *fakeDriver) AddForward(_ context.Context, host string, l, r int) error {
	f.calls = append(f.calls, fwdLog("add", host, l, r))
	if f.addErr != nil {
		return f.addErr
	}
	f.installed[host] = append(f.installed[host], livePair{Local: l, Remote: r})
	return nil
}

func (f *fakeDriver) CancelForward(_ context.Context, host string, l, r int) error {
	f.calls = append(f.calls, fwdLog("cancel", host, l, r))
	cur := f.installed[host]
	for i, p := range cur {
		if p.Local == l && p.Remote == r {
			f.installed[host] = append(cur[:i], cur[i+1:]...)
			return nil
		}
	}
	return nil
}

func fwdLog(op, host string, l, r int) string {
	return op + " " + host + " " + itoa(l) + ":" + itoa(r)
}

func itoa(i int) string {
	const digits = "0123456789"
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{digits[i%10]}, b...)
		i /= 10
	}
	return string(b)
}

func TestReconcile_freshStartCreatesMaster(t *testing.T) {
	t.Parallel()
	d := newFakeDriver()
	state := &State{Forwards: map[string][]Forward{
		KartKey("alpha", "web"): {{Local: 3000, Remote: 3000, Source: SourceExplicit}},
	}}
	report, live, err := Reconcile(context.Background(), state, nil, d, ReconcileOptions{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if !d.alive["drift.alpha.web"] {
		t.Errorf("expected master started")
	}
	if got := live.get("drift.alpha.web"); !reflect.DeepEqual(got, []livePair{{Local: 3000, Remote: 3000}}) {
		t.Errorf("cache: got %+v, want one pair", got)
	}
	if len(report.Errors) != 0 {
		t.Errorf("report.Errors: %v", report.Errors)
	}
}

func TestReconcile_idempotentSecondPass(t *testing.T) {
	t.Parallel()
	d := newFakeDriver()
	state := &State{Forwards: map[string][]Forward{
		KartKey("alpha", "web"): {{Local: 3000, Remote: 3000}},
	}}
	_, live, err := Reconcile(context.Background(), state, nil, d, ReconcileOptions{})
	if err != nil {
		t.Fatalf("first pass: %v", err)
	}
	prevCalls := len(d.calls)
	_, _, err = Reconcile(context.Background(), state, live, d, ReconcileOptions{})
	if err != nil {
		t.Fatalf("second pass: %v", err)
	}
	// Second pass: just one Check call, no add/start.
	if len(d.calls)-prevCalls != 1 {
		t.Errorf("idle reconcile should be 1 Check call, got %d new calls: %v", len(d.calls)-prevCalls, d.calls[prevCalls:])
	}
}

func TestReconcile_removalStopsMaster(t *testing.T) {
	t.Parallel()
	d := newFakeDriver()
	d.alive["drift.alpha.web"] = true
	d.installed["drift.alpha.web"] = []livePair{{Local: 3000, Remote: 3000}}
	live := &liveCache{Hosts: map[string]liveHost{
		"drift.alpha.web": {Forwards: []livePair{{Local: 3000, Remote: 3000}}},
	}}
	state := &State{Forwards: map[string][]Forward{}}
	_, updated, err := Reconcile(context.Background(), state, live, d, ReconcileOptions{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if d.alive["drift.alpha.web"] {
		t.Errorf("expected master stopped")
	}
	if len(updated.Hosts) != 0 {
		t.Errorf("cache should be empty, got %+v", updated.Hosts)
	}
}

func TestReconcile_diffAddCancel(t *testing.T) {
	t.Parallel()
	d := newFakeDriver()
	d.alive["drift.alpha.web"] = true
	d.installed["drift.alpha.web"] = []livePair{
		{Local: 3000, Remote: 3000},
		{Local: 5432, Remote: 5432},
	}
	live := &liveCache{Hosts: map[string]liveHost{
		"drift.alpha.web": {Forwards: []livePair{
			{Local: 3000, Remote: 3000},
			{Local: 5432, Remote: 5432},
		}},
	}}
	// Want: keep 3000, remap 5432→5433, add 8080.
	state := &State{Forwards: map[string][]Forward{
		KartKey("alpha", "web"): {
			{Local: 3000, Remote: 3000},
			{Local: 5433, Remote: 5432, RemappedFrom: 5432},
			{Local: 8080, Remote: 8080},
		},
	}}
	_, updated, err := Reconcile(context.Background(), state, live, d, ReconcileOptions{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}

	got := updated.get("drift.alpha.web")
	sort.Slice(got, func(i, j int) bool { return got[i].Remote < got[j].Remote })
	want := []livePair{
		{Local: 3000, Remote: 3000},
		{Local: 5433, Remote: 5432},
		{Local: 8080, Remote: 8080},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cache: got %+v, want %+v", got, want)
	}

	// 5432→5433 came across as cancel-then-add; 3000 should not be touched.
	for _, c := range d.calls {
		if c == "add drift.alpha.web 3000:3000" {
			t.Errorf("3000 should not have been re-added: %v", d.calls)
		}
	}
}

func TestReconcile_deadMasterRestarts(t *testing.T) {
	t.Parallel()
	d := newFakeDriver()
	// Cache says we have a forward, but Check reports the master gone.
	live := &liveCache{Hosts: map[string]liveHost{
		"drift.alpha.web": {Forwards: []livePair{{Local: 3000, Remote: 3000}}},
	}}
	state := &State{Forwards: map[string][]Forward{
		KartKey("alpha", "web"): {{Local: 3000, Remote: 3000}},
	}}
	_, updated, err := Reconcile(context.Background(), state, live, d, ReconcileOptions{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	// Master should be started fresh and the forward re-added.
	started := false
	for _, c := range d.calls {
		if c == "start drift.alpha.web" {
			started = true
		}
	}
	if !started {
		t.Errorf("expected master restart, calls: %v", d.calls)
	}
	if got := updated.get("drift.alpha.web"); len(got) != 1 {
		t.Errorf("cache: want one pair, got %+v", got)
	}
}

func TestReconcile_onlyKartScopes(t *testing.T) {
	t.Parallel()
	d := newFakeDriver()
	state := &State{Forwards: map[string][]Forward{
		KartKey("alpha", "web"): {{Local: 3000, Remote: 3000}},
		KartKey("beta", "api"):  {{Local: 8080, Remote: 8080}},
	}}
	_, _, err := Reconcile(context.Background(), state, nil, d,
		ReconcileOptions{OnlyKart: KartKey("alpha", "web")})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if d.alive["drift.beta.api"] {
		t.Errorf("beta/api should not have been touched")
	}
	if !d.alive["drift.alpha.web"] {
		t.Errorf("alpha/web should have been started")
	}
}

func TestReconcile_addErrorReportsButContinues(t *testing.T) {
	t.Parallel()
	d := newFakeDriver()
	d.addErr = errors.New("boom")
	state := &State{Forwards: map[string][]Forward{
		KartKey("alpha", "web"): {{Local: 3000, Remote: 3000}},
		KartKey("beta", "api"):  {{Local: 8080, Remote: 8080}},
	}}
	report, _, err := Reconcile(context.Background(), state, nil, d, ReconcileOptions{})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	if len(report.Errors) != 2 {
		t.Errorf("want 2 errors (one per kart), got %d: %v", len(report.Errors), report.Errors)
	}
}
