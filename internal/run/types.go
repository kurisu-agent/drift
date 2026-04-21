// Package run parses ~/.drift/runs.yaml and renders command templates.
// Server-side only — the client receives fully-rendered command strings
// via the run.resolve RPC.
package run

import "github.com/kurisu-agent/drift/internal/wire"

// Mode and PostHook are protocol-layer enums; this package re-exports the
// wire types so callers don't have to import wire just to read a field.
type (
	Mode     = wire.RunMode
	PostHook = wire.RunPostHook
)

const (
	ModeInteractive = wire.RunModeInteractive
	ModeOutput      = wire.RunModeOutput

	PostNone                = wire.RunPostNone
	PostConnectLastScaffold = wire.RunPostConnectLastScaffold
)
