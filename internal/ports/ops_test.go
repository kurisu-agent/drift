package ports

import (
	"testing"
)

func allFreeProber() LocalProber { return LocalProberFunc(func(int) bool { return true }) }

func freeExceptProber(busy ...int) LocalProber {
	set := map[int]bool{}
	for _, p := range busy {
		set[p] = true
	}
	return LocalProberFunc(func(p int) bool { return !set[p] })
}

func TestAddForward_picksRequestedWhenFree(t *testing.T) {
	t.Parallel()
	s := &State{}
	res, err := AddForward(s, allFreeProber(), "alpha", "web", 3000, 0, SourceExplicit)
	if err != nil {
		t.Fatalf("AddForward: %v", err)
	}
	if res.Remapped {
		t.Errorf("did not expect remap")
	}
	if res.Forward.Local != 3000 || res.Forward.Remote != 3000 {
		t.Errorf("unexpected forward: %+v", res.Forward)
	}
}

func TestAddForward_remapsOnConflict(t *testing.T) {
	t.Parallel()
	s := &State{}
	prober := freeExceptProber(5432)
	res, err := AddForward(s, prober, "alpha", "web", 5432, 0, SourceExplicit)
	if err != nil {
		t.Fatalf("AddForward: %v", err)
	}
	if !res.Remapped {
		t.Errorf("expected remap")
	}
	if res.Forward.Local != 5433 || res.Forward.RemappedFrom != 5432 {
		t.Errorf("unexpected forward: %+v", res.Forward)
	}
}

func TestAddForward_idempotent(t *testing.T) {
	t.Parallel()
	s := &State{}
	if _, err := AddForward(s, allFreeProber(), "alpha", "web", 3000, 0, SourceExplicit); err != nil {
		t.Fatalf("first add: %v", err)
	}
	res, err := AddForward(s, allFreeProber(), "alpha", "web", 3000, 0, SourceExplicit)
	if err != nil {
		t.Fatalf("second add: %v", err)
	}
	if !res.NoOp {
		t.Errorf("expected NoOp on duplicate add")
	}
	if got := s.Get("alpha", "web"); len(got) != 1 {
		t.Errorf("want 1 entry, got %d", len(got))
	}
}

func TestAddForward_avoidsCrossKartCollision(t *testing.T) {
	t.Parallel()
	s := &State{}
	if _, err := AddForward(s, allFreeProber(), "alpha", "web", 3000, 0, SourceExplicit); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// beta/api wants local=3000, remote=4000 — that local is taken by alpha.
	res, err := AddForward(s, allFreeProber(), "beta", "api", 4000, 3000, SourceExplicit)
	if err != nil {
		t.Fatalf("AddForward: %v", err)
	}
	if !res.Remapped || res.Forward.Local == 3000 {
		t.Errorf("expected cross-kart remap, got %+v", res.Forward)
	}
}

func TestRemoveForward_byRemote(t *testing.T) {
	t.Parallel()
	s := &State{}
	_, _ = AddForward(s, allFreeProber(), "alpha", "web", 3000, 0, SourceExplicit)
	if _, err := RemoveForward(s, "alpha", "web", 3000); err != nil {
		t.Fatalf("RemoveForward: %v", err)
	}
	if got := s.Get("alpha", "web"); len(got) != 0 {
		t.Errorf("want empty, got %+v", got)
	}
}

func TestRemoveForward_missing(t *testing.T) {
	t.Parallel()
	s := &State{}
	if _, err := RemoveForward(s, "alpha", "web", 3000); err == nil {
		t.Errorf("expected error for missing forward")
	}
}

func TestRemapForward(t *testing.T) {
	t.Parallel()
	s := &State{}
	_, _ = AddForward(s, allFreeProber(), "alpha", "web", 5432, 0, SourceExplicit)
	got, err := RemapForward(s, allFreeProber(), "alpha", "web", 5432, 5433)
	if err != nil {
		t.Fatalf("RemapForward: %v", err)
	}
	if got.Local != 5433 || got.RemappedFrom != 5432 {
		t.Errorf("unexpected: %+v", got)
	}

	// Clear the remap (newLocal=0 → snap to remote).
	got, err = RemapForward(s, allFreeProber(), "alpha", "web", 5432, 0)
	if err != nil {
		t.Fatalf("clear remap: %v", err)
	}
	if got.Local != 5432 || got.RemappedFrom != 0 {
		t.Errorf("expected cleared remap, got %+v", got)
	}
}

func TestUnionDevcontainer(t *testing.T) {
	t.Parallel()
	s := &State{}
	// Pre-existing explicit remap of 5432→5433.
	_, _ = AddForward(s, allFreeProber(), "alpha", "web", 5432, 0, SourceExplicit)
	if _, err := RemapForward(s, allFreeProber(), "alpha", "web", 5432, 5433); err != nil {
		t.Fatalf("seed remap: %v", err)
	}

	// Devcontainer spec lists 5432 (clobber attempt) and 3000 (new).
	if _, err := UnionDevcontainer(s, allFreeProber(), "alpha", "web", []int{5432, 3000}); err != nil {
		t.Fatalf("UnionDevcontainer: %v", err)
	}

	got := s.Get("alpha", "web")
	if len(got) != 2 {
		t.Fatalf("want 2 forwards, got %d: %+v", len(got), got)
	}
	for _, f := range got {
		if f.Remote == 5432 {
			if f.Local != 5433 || f.Source != SourceExplicit {
				t.Errorf("explicit remap was clobbered: %+v", f)
			}
		}
		if f.Remote == 3000 {
			if f.Source != SourceDevcontainer {
				t.Errorf("3000 should be devcontainer source: %+v", f)
			}
		}
	}

	// Re-run with 5432 dropped from spec — it should get pruned only if
	// it were devcontainer-source. Explicit one survives.
	if _, err := UnionDevcontainer(s, allFreeProber(), "alpha", "web", []int{3000}); err != nil {
		t.Fatalf("UnionDevcontainer 2: %v", err)
	}
	if got := s.Get("alpha", "web"); len(got) != 2 {
		t.Errorf("explicit 5432 should survive prune; got %+v", got)
	}
}

func TestUnionDevcontainer_prunesOldDevcontainerEntries(t *testing.T) {
	t.Parallel()
	s := &State{}
	// First connect populates 3000 + 5432 from devcontainer.
	if _, err := UnionDevcontainer(s, allFreeProber(), "alpha", "web", []int{3000, 5432}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	// Spec drops 5432.
	if _, err := UnionDevcontainer(s, allFreeProber(), "alpha", "web", []int{3000}); err != nil {
		t.Fatalf("union: %v", err)
	}
	got := s.Get("alpha", "web")
	if len(got) != 1 || got[0].Remote != 3000 {
		t.Errorf("want only 3000 left, got %+v", got)
	}
}
