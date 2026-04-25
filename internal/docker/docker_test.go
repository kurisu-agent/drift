package docker_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kurisu-agent/drift/internal/docker"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

type fakeRunner struct {
	calls []driftexec.Cmd
	reply driftexec.Result
	err   error
}

func (f *fakeRunner) Run(_ context.Context, cmd driftexec.Cmd) (driftexec.Result, error) {
	f.calls = append(f.calls, cmd)
	return f.reply, f.err
}

func TestStatusByLabelParsesAndKeysOnLabelValue(t *testing.T) {
	t.Parallel()
	f := &fakeRunner{reply: driftexec.Result{Stdout: []byte(
		"default-aa-001 running\ndefault-bb-002 exited\n",
	)}}
	c := &docker.Client{Runner: f}
	got, err := c.StatusByLabel(t.Context(), "dev.containers.id")
	if err != nil {
		t.Fatalf("StatusByLabel: %v", err)
	}
	want := map[string]string{
		"default-aa-001": "running",
		"default-bb-002": "exited",
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("map mismatch (-want +got):\n%s", diff)
	}
	if len(f.calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(f.calls))
	}
	wantArgs := []string{
		"ps", "-a",
		"--filter", "label=dev.containers.id",
		"--format", `{{.Label "dev.containers.id"}} {{.State}}`,
	}
	if diff := cmp.Diff(wantArgs, f.calls[0].Args); diff != "" {
		t.Errorf("args mismatch (-want +got):\n%s", diff)
	}
}

func TestStatusByLabelPrefersLivelierStateOnDuplicate(t *testing.T) {
	t.Parallel()
	// Same UID appearing twice: a recreated workspace whose old container
	// is still in the table as "exited" while the new one is running. The
	// running entry must win regardless of input order.
	f := &fakeRunner{reply: driftexec.Result{Stdout: []byte(
		"default-aa-001 exited\ndefault-aa-001 running\n",
	)}}
	c := &docker.Client{Runner: f}
	got, err := c.StatusByLabel(t.Context(), "dev.containers.id")
	if err != nil {
		t.Fatal(err)
	}
	if got["default-aa-001"] != "running" {
		t.Errorf("dedup picked %q, want running", got["default-aa-001"])
	}

	f = &fakeRunner{reply: driftexec.Result{Stdout: []byte(
		"default-aa-001 running\ndefault-aa-001 exited\n",
	)}}
	c = &docker.Client{Runner: f}
	got, err = c.StatusByLabel(t.Context(), "dev.containers.id")
	if err != nil {
		t.Fatal(err)
	}
	if got["default-aa-001"] != "running" {
		t.Errorf("dedup (reverse order) picked %q, want running", got["default-aa-001"])
	}
}

func TestStatusByLabelEmptyOutputReturnsEmptyMap(t *testing.T) {
	t.Parallel()
	f := &fakeRunner{reply: driftexec.Result{Stdout: []byte("\n")}}
	c := &docker.Client{Runner: f}
	got, err := c.StatusByLabel(t.Context(), "dev.containers.id")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Errorf("got %d entries on empty output, want 0", len(got))
	}
}

func TestStatusByLabelPropagatesExecError(t *testing.T) {
	t.Parallel()
	f := &fakeRunner{err: errors.New("docker not running")}
	c := &docker.Client{Runner: f}
	if _, err := c.StatusByLabel(t.Context(), "dev.containers.id"); err == nil {
		t.Fatal("StatusByLabel returned nil on exec error")
	}
}

func TestStatusByLabelRequiresLabel(t *testing.T) {
	t.Parallel()
	c := &docker.Client{Runner: &fakeRunner{}}
	if _, err := c.StatusByLabel(t.Context(), ""); err == nil {
		t.Fatal("StatusByLabel allowed empty label")
	}
}
