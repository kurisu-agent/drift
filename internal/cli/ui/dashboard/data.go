package dashboard

import "time"

// StatusSnapshot is the at-a-glance bundle the status tab renders. Live
// and demo data sources both produce this shape so panel rendering has
// one input format.
type StatusSnapshot struct {
	DriftVersion      string
	CircuitsTotal     int
	CircuitsReachable int
	KartsTotal        int
	KartsRunning      int
	PortsActive       int
	Activity          []ActivityEntry
}

// ActivityEntry is one row of the status tab's recent-activity table.
type ActivityEntry struct {
	When   time.Time
	Action string // "drift new", "kart restart", etc.
	Kart   string // empty for global actions
	Detail string // optional context (clone source, port mapping, ...)
}

// KartRow is one row of the karts tab.
type KartRow struct {
	Circuit   string
	Name      string
	Status    string // running / stopped / stale / error
	Source    string
	Tune      string
	Autostart bool
	LastUsed  time.Time
}

// CircuitRow is one row of the circuits tab.
type CircuitRow struct {
	Name      string
	Host      string
	Default   bool
	Lakitu    string // server version
	LatencyMS int64
	Reachable bool
	// Color is the optional per-circuit accent (hex like "#6B50FF") set
	// in the workstation's circuits config. The circuits panel renders
	// it as a small swatch next to the name; when the dashboard is
	// anchored to a single circuit, the brand accent derives from this
	// (theme.WithAccent). Empty = no tint.
	Color string
}

// ResourceRow is the shared shape for chest, characters, and tunes —
// the three read-only resource panels. Description fills the second
// column; UsedBy is a comma-joined list of kart names.
type ResourceRow struct {
	Circuit     string
	Name        string
	Description string
	UsedBy      string
}

// PortRow describes one workstation port forward.
type PortRow struct {
	Local   int
	Remote  int
	Circuit string
	Kart    string
	Active  bool
}
