package wire

// RunMode is the dispatch kind for a remote command shape. The client
// reads this to decide transport (mosh for interactive, plain ssh for
// output) and how to render results. Shared across `drift run`,
// `drift ai`, and `drift skill`.
type RunMode string

const (
	RunModeInteractive RunMode = "interactive"
	RunModeOutput      RunMode = "output"
)

// RunPostHook names a client-side post-exit hook. New hooks require a
// client release; new callers do not.
type RunPostHook string

const (
	RunPostNone                RunPostHook = ""
	RunPostConnectLastScaffold RunPostHook = "connect-last-scaffold"
)
