package drift

import (
	"bytes"
	"testing"

	"github.com/kurisu-agent/drift/internal/ports"
)

// boundProber reports IsFree=false for any port in the bound set.
type boundProber map[int]bool

func (b boundProber) IsFree(port int) bool { return !b[port] }

func TestUnionDevcontainerWithPrompts_acceptRemap(t *testing.T) {
	t.Parallel()
	state := &ports.State{}
	prober := boundProber{3000: true} // workstation has 3000 bound externally

	var asked []int
	prompt := func(remote, proposed int) (bool, error) {
		asked = append(asked, remote, proposed)
		return true, nil // accept
	}

	io := IO{Stderr: &bytes.Buffer{}}
	if err := unionDevcontainerWithPrompts(io, state, prober, "alpha", "web", []int{3000, 5432}, prompt); err != nil {
		t.Fatalf("union: %v", err)
	}

	got := state.Get("alpha", "web")
	if len(got) != 2 {
		t.Fatalf("want 2 forwards, got %+v", got)
	}
	for _, f := range got {
		if f.Source != ports.SourceDevcontainer {
			t.Errorf("want devcontainer source, got %s for %+v", f.Source, f)
		}
		switch f.Remote {
		case 3000:
			if f.Local == 3000 || f.RemappedFrom != 3000 {
				t.Errorf("3000 should have been remapped, got %+v", f)
			}
		case 5432:
			if f.Local != 5432 || f.RemappedFrom != 0 {
				t.Errorf("5432 should bind directly, got %+v", f)
			}
		}
	}
	if len(asked) != 2 || asked[0] != 3000 {
		t.Errorf("expected one prompt for :3000, got asks=%v", asked)
	}
}

func TestUnionDevcontainerWithPrompts_declineSkips(t *testing.T) {
	t.Parallel()
	state := &ports.State{}
	prober := boundProber{3000: true}

	prompt := func(remote, proposed int) (bool, error) {
		return false, nil // decline
	}

	stderr := &bytes.Buffer{}
	io := IO{Stderr: stderr}
	if err := unionDevcontainerWithPrompts(io, state, prober, "alpha", "web", []int{3000, 5432}, prompt); err != nil {
		t.Fatalf("union: %v", err)
	}

	got := state.Get("alpha", "web")
	if len(got) != 1 {
		t.Fatalf("want only 5432 added, got %+v", got)
	}
	if got[0].Remote != 5432 {
		t.Errorf("expected 5432 to survive, got %+v", got[0])
	}
	if !bytes.Contains(stderr.Bytes(), []byte("skipped :3000")) {
		t.Errorf("expected stderr message about skipping :3000, got %q", stderr.String())
	}
}

func TestUnionDevcontainerWithPrompts_silentFallback(t *testing.T) {
	t.Parallel()
	state := &ports.State{}
	prober := boundProber{3000: true}

	io := IO{Stderr: &bytes.Buffer{}}
	if err := unionDevcontainerWithPrompts(io, state, prober, "alpha", "web", []int{3000}, nil); err != nil {
		t.Fatalf("union: %v", err)
	}

	got := state.Get("alpha", "web")
	if len(got) != 1 {
		t.Fatalf("want 1 forward, got %+v", got)
	}
	if got[0].Local == 3000 || got[0].RemappedFrom != 3000 {
		t.Errorf("silent path should auto-remap :3000, got %+v", got[0])
	}
}

func TestUnionDevcontainerWithPrompts_explicitRemapPreserved(t *testing.T) {
	t.Parallel()
	state := &ports.State{}
	if _, err := ports.AddForward(state, ports.LocalProberFunc(func(int) bool { return true }),
		"alpha", "web", 5432, 5433, ports.SourceExplicit); err != nil {
		t.Fatalf("seed: %v", err)
	}

	called := false
	prompt := func(remote, proposed int) (bool, error) {
		called = true
		return true, nil
	}

	io := IO{Stderr: &bytes.Buffer{}}
	prober := boundProber{} // nothing bound on workstation
	if err := unionDevcontainerWithPrompts(io, state, prober, "alpha", "web", []int{5432, 3000}, prompt); err != nil {
		t.Fatalf("union: %v", err)
	}

	got := state.Get("alpha", "web")
	if len(got) != 2 {
		t.Fatalf("want 2 forwards, got %+v", got)
	}
	for _, f := range got {
		if f.Remote == 5432 {
			if f.Local != 5433 || f.Source != ports.SourceExplicit {
				t.Errorf("explicit 5432→5433 was clobbered: %+v", f)
			}
		}
	}
	if called {
		t.Errorf("prompt should not fire for an already-mapped port")
	}
}
