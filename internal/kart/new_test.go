package kart

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/devpod"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// recorder is a driftexec.Runner that captures every invocation so tests can
// assert on argv without needing a real devpod binary on PATH.
type recorder struct {
	calls []driftexec.Cmd
	// listStdout overrides the response to `devpod list`. Empty string
	// means "no workspaces" (returns `[]`). Tests that need a specific
	// workspace to appear as known to devpod set this explicitly.
	listStdout string
}

func (r *recorder) Run(_ context.Context, cmd driftexec.Cmd) (driftexec.Result, error) {
	r.calls = append(r.calls, cmd)
	switch {
	case len(cmd.Args) > 0 && cmd.Args[0] == "list":
		out := r.listStdout
		if out == "" {
			out = "[]"
		}
		return driftexec.Result{Stdout: []byte(out)}, nil
	case len(cmd.Args) > 0 && cmd.Args[0] == "up":
		return driftexec.Result{Stdout: []byte(`{}`)}, nil
	case len(cmd.Args) > 0 && cmd.Args[0] == "agent":
		return driftexec.Result{}, nil
	default:
		return driftexec.Result{}, nil
	}
}

func (r *recorder) upCalls() []driftexec.Cmd {
	var out []driftexec.Cmd
	for _, c := range r.calls {
		if len(c.Args) > 0 && c.Args[0] == "up" {
			out = append(out, c)
		}
	}
	return out
}

func TestNewRejectsCollision(t *testing.T) {
	// Real collision: garage dir AND devpod both know the workspace.
	garage := t.TempDir()
	if err := os.MkdirAll(config.KartDir(garage, "dup"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{listStdout: `[{"id":"dup"}]`}
	deps := NewDeps{
		GarageDir: garage,
		Devpod:    &devpod.Client{Runner: driftexec.RunnerFunc(rec.Run)},
		Resolver: &Resolver{
			LoadTune:      func(string) (*Tune, error) { return &Tune{}, nil },
			LoadCharacter: func(string) (*Character, error) { return &Character{}, nil },
		},
	}
	_, err := New(context.Background(), deps, Flags{Name: "dup"})
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != rpcerr.TypeNameCollision {
		t.Fatalf("expected name_collision, got %v", err)
	}
}

func TestNewDetectsStaleGarageCorpse(t *testing.T) {
	// Stale corpse: garage dir exists (crashed previous `drift new`) but
	// devpod knows nothing. Next attempt must return stale_kart with a
	// suggestion the user can act on.
	garage := t.TempDir()
	if err := os.MkdirAll(config.KartDir(garage, "ghost"), 0o755); err != nil {
		t.Fatal(err)
	}
	rec := &recorder{} // default listStdout "" → recorder returns "[]"
	deps := NewDeps{
		GarageDir: garage,
		Devpod:    &devpod.Client{Runner: driftexec.RunnerFunc(rec.Run)},
		Resolver: &Resolver{
			LoadTune:      func(string) (*Tune, error) { return &Tune{}, nil },
			LoadCharacter: func(string) (*Character, error) { return &Character{}, nil },
		},
	}
	_, err := New(context.Background(), deps, Flags{Name: "ghost"})
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != rpcerr.TypeStaleKart {
		t.Fatalf("expected stale_kart, got %v", err)
	}
	if _, ok := re.Data["suggestion"]; !ok {
		t.Fatalf("stale_kart error should carry a suggestion field: %+v", re.Data)
	}
}

func TestNewInvalidName(t *testing.T) {
	garage := t.TempDir()
	deps := NewDeps{
		GarageDir: garage,
		Devpod:    &devpod.Client{},
		Resolver:  &Resolver{},
	}
	_, err := New(context.Background(), deps, Flags{Name: "Invalid Name"})
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != rpcerr.TypeInvalidName {
		t.Fatalf("expected invalid_name, got %v", err)
	}
}

func TestNewClonePathAndConfig(t *testing.T) {
	garage := t.TempDir()
	rec := &recorder{}
	fixedTime := time.Date(2026, 4, 17, 12, 0, 0, 0, time.UTC)

	deps := NewDeps{
		GarageDir: garage,
		Devpod:    &devpod.Client{Runner: driftexec.RunnerFunc(rec.Run)},
		Resolver: &Resolver{
			Defaults:      ServerDefaults{DefaultTune: "default", DefaultCharacter: "kurisu"},
			LoadTune:      func(string) (*Tune, error) { return &Tune{}, nil },
			LoadCharacter: func(string) (*Character, error) { return &Character{GitName: "K", GitEmail: "k@x"}, nil },
		},
		Now: func() time.Time { return fixedTime },
	}
	res, err := New(context.Background(), deps, Flags{
		Name:      "myproj",
		Clone:     "https://example.com/repo.git",
		Autostart: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Source.Mode != "clone" || res.Source.URL != "https://example.com/repo.git" {
		t.Fatalf("source wrong: %+v", res.Source)
	}
	if !res.Autostart {
		t.Fatalf("autostart not set")
	}

	// devpod up invocation must include the clone URL as positional source
	// (after --id=myproj).
	ups := rec.upCalls()
	if len(ups) != 1 {
		t.Fatalf("expected one `devpod up` call, got %d: %+v", len(ups), rec.calls)
	}
	args := ups[0].Args
	foundURL := false
	for _, a := range args {
		if a == "https://example.com/repo.git" {
			foundURL = true
		}
	}
	if !foundURL {
		t.Fatalf("clone URL not passed to devpod up: %v", args)
	}

	// Config.yaml landed with the right fields.
	cfg := config.KartConfigPath(garage, "myproj")
	buf, err := os.ReadFile(cfg)
	if err != nil {
		t.Fatal(err)
	}
	if !contains(buf, "repo: https://example.com/repo.git") {
		t.Fatalf("config.yaml missing repo: %s", buf)
	}
	if !contains(buf, "source_mode: clone") {
		t.Fatalf("config.yaml missing source_mode: %s", buf)
	}
	if !contains(buf, "character: kurisu") {
		t.Fatalf("config.yaml missing character: %s", buf)
	}
	// Autostart marker (legacy sentinel; the authoritative field now lives
	// on config.yaml via cluster 25, but the sentinel still lands for one
	// release so older readers keep working).
	if _, err := os.Stat(config.KartAutostartPath(garage, "myproj")); err != nil {
		t.Fatalf("autostart marker missing: %v", err)
	}
	if !contains(buf, "autostart: true") {
		t.Fatalf("config.yaml missing autostart field: %s", buf)
	}
}

func TestNewClonePathPersistsSourceUnderKartDir(t *testing.T) {
	// When the kart resolves a github URL clone with a PAT, drift pre-clones
	// server-side and hands devpod a local path. That local path needs to
	// outlive kart.new — devpod records it verbatim in workspace.json and
	// references it on every subsequent up/start. A scratch path would
	// vanish at the end of kart.new and break drift connect on the next
	// call (the bug fixed here).
	garage := t.TempDir()
	rec := &recorder{}
	// Capture the destDir Starter.Clone receives so we can assert it lives
	// under <garage>/karts/<name>/source rather than under /tmp scratch.
	var clonedTo string
	deps := NewDeps{
		GarageDir: garage,
		Devpod:    &devpod.Client{Runner: driftexec.RunnerFunc(rec.Run)},
		Starter: &Starter{Runner: driftexec.RunnerFunc(func(_ context.Context, cmd driftexec.Cmd) (driftexec.Result, error) {
			if cmd.Name == "git" && len(cmd.Args) >= 4 && cmd.Args[0] == "clone" {
				clonedTo = cmd.Args[len(cmd.Args)-1]
				if err := os.MkdirAll(clonedTo, 0o700); err != nil {
					return driftexec.Result{}, err
				}
			}
			return driftexec.Result{}, nil
		})},
		Resolver: &Resolver{
			LoadTune: func(string) (*Tune, error) { return &Tune{}, nil },
			LoadCharacter: func(string) (*Character, error) {
				return &Character{GitName: "K", GitEmail: "k@x", PAT: "github_pat_test"}, nil
			},
		},
	}
	_, err := New(context.Background(), deps, Flags{
		Name:      "githubkart",
		Clone:     "https://github.com/example-org/example.git",
		Character: "kurisu",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	wantPrefix := config.KartDir(garage, "githubkart")
	if clonedTo == "" {
		t.Fatal("Starter.Clone was never invoked; pre-clone path didn't fire")
	}
	if !strings.HasPrefix(clonedTo, wantPrefix+string(os.PathSeparator)) {
		t.Fatalf("clone target %q must live under kart dir %q", clonedTo, wantPrefix)
	}
	if _, statErr := os.Stat(clonedTo); statErr != nil {
		t.Fatalf("clone dir should still exist after kart.new returns: %v", statErr)
	}

	// devpod up's positional source must be the persistent local path,
	// not the original github URL — otherwise devpod would resolve via
	// its own clone and bypass our auth-injected pre-clone.
	ups := rec.upCalls()
	if len(ups) != 1 {
		t.Fatalf("expected one devpod up, got %d", len(ups))
	}
	sawLocalPath := false
	for _, a := range ups[0].Args {
		if a == clonedTo {
			sawLocalPath = true
		}
	}
	if !sawLocalPath {
		t.Fatalf("devpod up should reference the persistent clone path %q; args=%v", clonedTo, ups[0].Args)
	}
}

func TestNewDevpodFailureRollsBackGarageDir(t *testing.T) {
	// Post-`writeKartConfig` failures roll back: the garage dir is wiped
	// and `devpod delete` is best-effort-invoked. A stale `status: error`
	// tombstone only appears if RemoveAll itself fails — exercised
	// separately (can't force RemoveAll to fail portably in unit tests).
	garage := t.TempDir()

	rec := &failingRecorder{fail: true}
	deps := NewDeps{
		GarageDir: garage,
		Devpod:    &devpod.Client{Runner: driftexec.RunnerFunc(rec.Run)},
		Resolver: &Resolver{
			LoadTune:      func(string) (*Tune, error) { return &Tune{}, nil },
			LoadCharacter: func(string) (*Character, error) { return nil, nil },
		},
	}
	_, err := New(context.Background(), deps, Flags{
		Name:  "brokenkart",
		Clone: "https://example.com/r.git",
	})
	if err == nil {
		t.Fatalf("expected error")
	}
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != rpcerr.TypeDevpodUpFailed {
		t.Fatalf("expected devpod_up_failed, got %v", err)
	}
	kartDir := config.KartDir(garage, "brokenkart")
	if _, statErr := os.Stat(kartDir); !os.IsNotExist(statErr) {
		t.Fatalf("kart dir should be removed after rollback, stat err = %v", statErr)
	}
	// Rollback should have attempted `devpod delete` so a retry sees a
	// clean slate on both sides of the house.
	sawDelete := false
	for _, args := range rec.calls {
		if len(args) > 0 && args[0] == "delete" {
			sawDelete = true
			break
		}
	}
	if !sawDelete {
		t.Fatalf("rollback did not invoke `devpod delete`: %v", rec.calls)
	}
}

// failingRecorder is like recorder but fails `devpod up`.
type failingRecorder struct {
	fail  bool
	calls [][]string
}

func (r *failingRecorder) Run(_ context.Context, cmd driftexec.Cmd) (driftexec.Result, error) {
	r.calls = append(r.calls, append([]string{}, cmd.Args...))
	if len(cmd.Args) > 0 && cmd.Args[0] == "up" && r.fail {
		return driftexec.Result{}, errors.New("simulated devpod failure")
	}
	if len(cmd.Args) > 0 && cmd.Args[0] == "list" {
		return driftexec.Result{Stdout: []byte(`[]`)}, nil
	}
	return driftexec.Result{}, nil
}

func TestResultJSONShape(t *testing.T) {
	r := &Result{
		Name:      "myproj",
		Source:    KartSource{Mode: "clone", URL: "u"},
		Tune:      "default",
		Character: "k",
		Autostart: true,
		CreatedAt: "2026-04-17T12:00:00Z",
	}
	buf, err := json.Marshal(r)
	if err != nil {
		t.Fatal(err)
	}
	var probe map[string]any
	if err := json.Unmarshal(buf, &probe); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"name", "source", "autostart", "created_at"} {
		if _, ok := probe[k]; !ok {
			t.Fatalf("missing JSON field %q: %s", k, buf)
		}
	}
}

func contains(b []byte, sub string) bool {
	return stringContains(string(b), sub)
}

func stringContains(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
