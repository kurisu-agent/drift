package drift

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/kurisu-agent/drift/internal/wire"
)

// stubKartList replies to MethodKartList with the given names; every
// other method falls through to the caller-supplied next hook so each
// test can layer its own kart.delete behaviour on top.
func stubKartList(names []string, next func(ctx context.Context, circuit, method string, params, out any) error) func(context.Context, string, string, any, any) error {
	return func(ctx context.Context, circuit, method string, params, out any) error {
		if method == wire.MethodKartList {
			entries := make([]listEntry, len(names))
			for i, n := range names {
				entries[i].Name = n
				entries[i].Status = "running"
			}
			payload, _ := json.Marshal(listResult{Karts: entries})
			*(out.(*json.RawMessage)) = json.RawMessage(payload)
			return nil
		}
		return next(ctx, circuit, method, params, out)
	}
}

func TestIsGlobPattern(t *testing.T) {
	cases := []struct {
		in   string
		want bool
	}{
		{"alpha", false},
		{"", false},
		{"test-*", true},
		{"k?", true},
		{"a[bc]", true},
		{"a*b?c[d]", true},
	}
	for _, c := range cases {
		if got := isGlobPattern(c.in); got != c.want {
			t.Errorf("isGlobPattern(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestRunKartDelete_GlobMatchesAndDeletesInParallel(t *testing.T) {
	var deleted sync.Map
	var inflight, peak atomic.Int32
	all := []string{"nixenv1", "nixenv2", "nixenv3", "alpha", "beta"}
	d, _ := newKartDeps(t, stubKartList(all, func(_ context.Context, _, method string, params, out any) error {
		if method != wire.MethodKartDelete {
			return errors.New("unexpected method " + method)
		}
		// Track concurrent in-flight deletes to confirm the sliding
		// window actually overlaps RPCs (not strictly serial).
		cur := inflight.Add(1)
		for {
			p := peak.Load()
			if cur <= p || peak.CompareAndSwap(p, cur) {
				break
			}
		}
		defer inflight.Add(-1)

		name := params.(map[string]string)["name"]
		deleted.Store(name, true)
		raw := json.RawMessage(`{"name":"` + name + `","status":"deleted"}`)
		*(out.(*json.RawMessage)) = raw
		return nil
	}))
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}
	cli := &CLI{}

	rc := runKartDelete(context.Background(), io, cli,
		deleteCmd{Name: "nixenv*", Force: true}, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr.String())
	}
	for _, want := range []string{"nixenv1", "nixenv2", "nixenv3"} {
		if _, ok := deleted.Load(want); !ok {
			t.Errorf("expected delete for %q", want)
		}
	}
	for _, skip := range []string{"alpha", "beta"} {
		if _, ok := deleted.Load(skip); ok {
			t.Errorf("unexpected delete for %q", skip)
		}
	}
	out := stdout.String()
	for _, want := range []string{
		`deleted kart "nixenv1" (status deleted)`,
		`deleted kart "nixenv2" (status deleted)`,
		`deleted kart "nixenv3" (status deleted)`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stdout missing %q\nfull: %s", want, out)
		}
	}
}

func TestRunKartDelete_GlobNoMatchExitsNonzero(t *testing.T) {
	d, _ := newKartDeps(t, stubKartList([]string{"alpha", "beta"}, func(context.Context, string, string, any, any) error {
		return errors.New("delete should not be called")
	}))
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}
	rc := runKartDelete(context.Background(), io, &CLI{},
		deleteCmd{Name: "nixenv*", Force: true}, d)
	if rc == 0 {
		t.Fatal("expected nonzero rc when no karts match")
	}
	if !strings.Contains(stderr.String(), "no karts") {
		t.Errorf("stderr = %q, want 'no karts ... match'", stderr.String())
	}
}

func TestRunKartDelete_GlobPartialFailureReportsAndExits1(t *testing.T) {
	d, _ := newKartDeps(t, stubKartList([]string{"k1", "k2", "k3"}, func(_ context.Context, _, method string, params, out any) error {
		if method != wire.MethodKartDelete {
			return errors.New("unexpected method " + method)
		}
		name := params.(map[string]string)["name"]
		if name == "k2" {
			return errors.New("kart busy")
		}
		raw := json.RawMessage(`{"name":"` + name + `","status":"deleted"}`)
		*(out.(*json.RawMessage)) = raw
		return nil
	}))
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}
	rc := runKartDelete(context.Background(), io, &CLI{},
		deleteCmd{Name: "k*", Force: true}, d)
	if rc != 1 {
		t.Fatalf("rc=%d (want 1) stderr=%s", rc, stderr.String())
	}
	if !strings.Contains(stderr.String(), "k2: kart busy") {
		t.Errorf("stderr missing k2 failure: %q", stderr.String())
	}
	if !strings.Contains(stderr.String(), "deleted 2/3") {
		t.Errorf("stderr missing summary: %q", stderr.String())
	}
	if !strings.Contains(stdout.String(), `deleted kart "k1"`) ||
		!strings.Contains(stdout.String(), `deleted kart "k3"`) {
		t.Errorf("stdout missing successful deletes: %q", stdout.String())
	}
}

func TestRunKartDelete_GlobJSONOutput(t *testing.T) {
	d, _ := newKartDeps(t, stubKartList([]string{"k1", "k2"}, func(_ context.Context, _, method string, params, out any) error {
		if method != wire.MethodKartDelete {
			return errors.New("unexpected method " + method)
		}
		name := params.(map[string]string)["name"]
		raw := json.RawMessage(`{"name":"` + name + `","status":"deleted"}`)
		*(out.(*json.RawMessage)) = raw
		return nil
	}))
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr}
	rc := runKartDelete(context.Background(), io, &CLI{Output: "json"},
		deleteCmd{Name: "k*", Force: true}, d)
	if rc != 0 {
		t.Fatalf("rc=%d stderr=%s", rc, stderr.String())
	}
	var got struct {
		Pattern string             `json:"pattern"`
		Circuit string             `json:"circuit"`
		Results []globDeleteResult `json:"results"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v\nstdout=%s", err, stdout.String())
	}
	if got.Circuit != "main" {
		t.Errorf("circuit = %q, want main", got.Circuit)
	}
	if len(got.Results) != 2 {
		t.Fatalf("results = %d, want 2", len(got.Results))
	}
	for _, r := range got.Results {
		if r.Status != "deleted" {
			t.Errorf("result %q status = %q, want deleted", r.Name, r.Status)
		}
	}
}

func TestRunKartDelete_GlobNonInteractiveRequiresForce(t *testing.T) {
	d, _ := newKartDeps(t, stubKartList([]string{"k1"}, func(context.Context, string, string, any, any) error {
		t.Fatal("delete should not be called without -y on non-tty")
		return nil
	}))
	var stdout, stderr bytes.Buffer
	io := IO{Stdout: &stdout, Stderr: &stderr, Stdin: strings.NewReader("")}
	rc := runKartDelete(context.Background(), io, &CLI{},
		deleteCmd{Name: "k*"}, d)
	if rc == 0 {
		t.Fatal("expected nonzero rc")
	}
	if !strings.Contains(stderr.String(), "-y on non-interactive stdin") {
		t.Errorf("stderr = %q", stderr.String())
	}
}

func TestMatchKartGlob_BadPattern(t *testing.T) {
	_, err := matchKartGlob("[abc", []listEntry{{Name: "x"}})
	if err == nil {
		t.Fatal("expected error for malformed pattern")
	}
	if !strings.Contains(err.Error(), "invalid glob pattern") {
		t.Errorf("err = %v", err)
	}
}
