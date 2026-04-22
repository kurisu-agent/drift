package wire

// RunMode is the dispatch kind for a remote command shape. The client
// reads this to decide transport (mosh for interactive, plain ssh for
// output) and how to render results.
//
// Name carries over from the retired `drift run` shorthand registry —
// the shared dispatch machinery now backs `drift ai` and
// `drift skill`, which reuse these types verbatim.
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
