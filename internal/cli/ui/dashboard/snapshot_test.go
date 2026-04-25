package dashboard

import (
	"context"
	"flag"
	"os"
	"path/filepath"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

var update = flag.Bool("update", false, "update golden frames under testdata/")

// runFrame drives a fresh model through Init + WindowSizeMsg + every
// command Init returned, and returns View().Content. Used to capture
// deterministic frames for golden snapshots.
func runFrame(t *testing.T, opts Options, w, h int, focus Tab) string {
	t.Helper()
	opts.Theme = &ui.Theme{Enabled: false}
	opts.InitialTab = focus
	m := newModel(opts)

	// Pump Init's tea.Cmds synchronously so async data loads land before
	// View() is captured. We don't call tea.NewProgram — we want a
	// deterministic snapshot, not a real terminal session.
	cmd := m.Init()
	deliver(t, m, cmd)

	resized, _ := m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	mm, _ := resized.(*model)
	return mm.View().Content
}

// deliver flattens a tea.Cmd (potentially a Batch) and feeds every
// emitted Msg into the model until the queue is empty or a per-cmd
// timeout fires. Long-running cmds (tea.Tick) are skipped — snapshots
// only care about the data-fetch responses, not the periodic ticker.
func deliver(t *testing.T, m tea.Model, cmd tea.Cmd) {
	t.Helper()
	queue := []tea.Cmd{cmd}
	for len(queue) > 0 {
		c := queue[0]
		queue = queue[1:]
		if c == nil {
			continue
		}
		msg := runCmd(c, 500*time.Millisecond)
		if msg == nil {
			continue
		}
		// Skip tickMsg — the snapshot harness doesn't simulate time.
		if _, ok := msg.(tickMsg); ok {
			continue
		}
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, sub := range batch {
				queue = append(queue, sub)
			}
			continue
		}
		next, follow := m.Update(msg)
		m = next
		if follow != nil {
			queue = append(queue, follow)
		}
	}
}

// runCmd executes a tea.Cmd with a per-call timeout so a tea.Tick or
// other long-blocking cmd can't stall the snapshot.
func runCmd(c tea.Cmd, d time.Duration) tea.Msg {
	ch := make(chan tea.Msg, 1)
	go func() { ch <- c() }()
	select {
	case msg := <-ch:
		return msg
	case <-time.After(d):
		return nil
	}
}

// emptyDS is the no-data data source for snapshots that intentionally
// render the empty-state copy.
type emptyDS struct{}

func (emptyDS) Status(_ context.Context) (StatusSnapshot, error)     { return StatusSnapshot{}, nil }
func (emptyDS) Karts(_ context.Context, _ string) ([]KartRow, error) { return nil, nil }
func (emptyDS) Circuits(_ context.Context) ([]CircuitRow, error)     { return nil, nil }
func (emptyDS) Chest(_ context.Context) ([]ResourceRow, error)       { return nil, nil }
func (emptyDS) Characters(_ context.Context) ([]ResourceRow, error)  { return nil, nil }
func (emptyDS) Tunes(_ context.Context) ([]ResourceRow, error)       { return nil, nil }
func (emptyDS) Ports(_ context.Context) ([]PortRow, error)           { return nil, nil }

// fixtureDS is a small inline fixture so the snapshot tests never
// depend on internal/demo (avoiding the dashboard <-> demo import
// cycle that bit us earlier).
type fixtureDS struct{}

func (fixtureDS) Status(_ context.Context) (StatusSnapshot, error) {
	return StatusSnapshot{
		DriftVersion:      "0.4.3",
		CircuitsTotal:     3,
		CircuitsReachable: 3,
		KartsTotal:        9,
		KartsRunning:      7,
		PortsActive:       4,
		Activity: []ActivityEntry{
			{When: time.Now().Add(-3 * time.Minute), Action: "drift new", Kart: "alpha.plan-14", Detail: "from example-org/template"},
			{When: time.Now().Add(-12 * time.Minute), Action: "kart restart", Kart: "alpha.api"},
			{When: time.Now().Add(-1 * time.Hour), Action: "port add", Kart: "alpha.web", Detail: ":3000 -> :3000"},
			{When: time.Now().Add(-2 * time.Hour), Action: "drift status"},
		},
	}, nil
}

func (fixtureDS) Karts(_ context.Context, _ string) ([]KartRow, error) {
	return []KartRow{
		{Circuit: "alpha", Name: "api", Status: "running", Source: "clone", Tune: "go-stack", Autostart: true},
		{Circuit: "alpha", Name: "web", Status: "running", Source: "clone", Tune: "ts-stack"},
		{Circuit: "alpha", Name: "db", Status: "stopped", Source: "starter", Tune: "py-stack"},
		{Circuit: "beta", Name: "experiments", Status: "running", Source: "clone", Tune: "py-stack"},
		{Circuit: "beta", Name: "scratch", Status: "stale"},
	}, nil
}

func (fixtureDS) Circuits(_ context.Context) ([]CircuitRow, error) {
	return []CircuitRow{
		{Name: "alpha", Host: "alpha.example.org", Default: true, Lakitu: "0.4.3", LatencyMS: 18, Reachable: true},
		{Name: "beta", Host: "beta.example.org", Lakitu: "0.4.3", LatencyMS: 42, Reachable: true},
	}, nil
}

func (fixtureDS) Chest(_ context.Context) ([]ResourceRow, error) {
	return []ResourceRow{
		{Circuit: "alpha", Name: "OPENAI_API_KEY", Description: "1password://drift/openai", UsedBy: "api, web"},
	}, nil
}
func (fixtureDS) Characters(_ context.Context) ([]ResourceRow, error) { return nil, nil }
func (fixtureDS) Tunes(_ context.Context) ([]ResourceRow, error)      { return nil, nil }
func (fixtureDS) Ports(_ context.Context) ([]PortRow, error)          { return nil, nil }

// TestSnapshots dumps every panel's first-frame output to testdata/
// at a fixed terminal size. With -update, the goldens are rewritten;
// without -update, the test asserts byte equality. Run with
//
//	go test ./internal/cli/ui/dashboard/... -run Snapshot -update
//
// after intentional UI changes, then commit the diff.
func TestSnapshots(t *testing.T) {
	t.Setenv("NO_COLOR", "1")

	dir := filepath.Join("testdata", "snapshots")
	if *update {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	cases := []struct {
		name string
		ds   DataSource
		tab  Tab
	}{
		{"empty-status", emptyDS{}, TabStatus},
		{"empty-karts", emptyDS{}, TabKarts},
		{"empty-circuits", emptyDS{}, TabCircuits},
		{"fixture-status", fixtureDS{}, TabStatus},
		{"fixture-karts", fixtureDS{}, TabKarts},
		{"fixture-circuits", fixtureDS{}, TabCircuits},
		{"fixture-chest", fixtureDS{}, TabChest},
		{"fixture-ports", fixtureDS{}, TabPorts},
		{"fixture-logs", fixtureDS{}, TabLogs},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			frame := runFrame(t, Options{
				DataSource:   tc.ds,
				DriftVersion: "test",
			}, 120, 30, tc.tab)
			path := filepath.Join(dir, tc.name+".txt")
			if *update {
				if err := os.WriteFile(path, []byte(frame), 0o644); err != nil {
					t.Fatal(err)
				}
				return
			}
			want, err := os.ReadFile(path)
			if err != nil {
				if os.IsNotExist(err) {
					t.Fatalf("missing golden %s — run with -update to create it", path)
				}
				t.Fatal(err)
			}
			if string(want) != frame {
				t.Errorf("frame mismatch for %s\n--- want ---\n%s\n--- got ---\n%s",
					tc.name, string(want), frame)
			}
		})
	}
}
