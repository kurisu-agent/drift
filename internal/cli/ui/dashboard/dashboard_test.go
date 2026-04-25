package dashboard

import (
	"context"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/kurisu-agent/drift/internal/cli/ui"
)

// fixedTheme returns a disabled-color theme so tests assert on plain text.
func fixedTheme() *ui.Theme { return &ui.Theme{Enabled: false} }

// emptyDataSource is a no-op dashboard.DataSource for model-only tests.
type emptyDataSource struct{}

func (emptyDataSource) Status(_ context.Context) (StatusSnapshot, error) {
	return StatusSnapshot{}, nil
}
func (emptyDataSource) Karts(_ context.Context, _ string) ([]KartRow, error) { return nil, nil }
func (emptyDataSource) Circuits(_ context.Context) ([]CircuitRow, error)     { return nil, nil }
func (emptyDataSource) Chest(_ context.Context) ([]ResourceRow, error)       { return nil, nil }
func (emptyDataSource) Characters(_ context.Context) ([]ResourceRow, error)  { return nil, nil }
func (emptyDataSource) Tunes(_ context.Context) ([]ResourceRow, error)       { return nil, nil }
func (emptyDataSource) Ports(_ context.Context) ([]PortRow, error)           { return nil, nil }

func TestDashboardTabsCycle(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newModel(Options{
		Theme:        fixedTheme(),
		DataSource:   emptyDataSource{},
		DriftVersion: "test",
	})
	if m.tab != TabStatus {
		t.Fatalf("initial tab = %v, want %v", m.tab, TabStatus)
	}
	out, _ := m.Update(tea.KeyPressMsg{Code: '\t'})
	m = out.(*model)
	if m.tab != TabKarts {
		t.Fatalf("after tab: tab = %v, want %v", m.tab, TabKarts)
	}
	out, _ = m.Update(tea.KeyPressMsg{Text: "3"})
	m = out.(*model)
	if m.tab != TabCircuits {
		t.Fatalf("after '3': tab = %v, want %v", m.tab, TabCircuits)
	}
}

func TestDashboardViewIncludesTabBar(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	m := newModel(Options{
		Theme:        fixedTheme(),
		DataSource:   emptyDataSource{},
		DriftVersion: "test",
	})
	m.width, m.height = 120, 40
	v := m.View()
	for _, want := range []string{"status", "karts", "circuits", "ports", "logs"} {
		if !strings.Contains(v.Content, want) {
			t.Errorf("tab bar missing %q", want)
		}
	}
}
