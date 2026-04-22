package drift

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/version"
)

// updateCheckInterval gates how often drift phones GitHub in the
// background. 24h is the sweet spot: short enough that the banner
// surfaces within a day of a release, long enough to never feel like a
// nag.
const updateCheckInterval = 24 * time.Hour

// updateCheckTimeout caps the background GET so a stalled GitHub doesn't
// keep the goroutine alive forever. The parent process usually outlives
// this anyway — short commands just let it die with them.
const updateCheckTimeout = 10 * time.Second

// fetchLatestReleaseFn is the indirection point tests swap out to drive
// the background check without touching the network.
var fetchLatestReleaseFn = fetchLatestRelease

// updateCheckRepo / updateCheckAPIBase mirror the defaults on updateCmd
// so the background check targets the same release feed as `drift
// update`. Kept as vars so tests can point both at a mock.
var (
	updateCheckRepo    = "kurisu-agent/drift"
	updateCheckAPIBase = "https://api.github.com"
)

// runPreDispatch is the pre-command hook fired after Kong parse, before
// the dispatch switch. It's the place for advisory messages that aren't
// tied to any single subcommand (update available, deprecation notices,
// once-a-day phone-home checks). All work is non-blocking — anything
// that needs the network fires a goroutine and the result lands in
// state.json for the *next* invocation to surface.
func runPreDispatch(io IO, cli *CLI, command string) {
	preHookUpdateCheck(io, cli, command)
	// Future pre-dispatch hooks go here. Each should be: cheap when
	// disabled, non-blocking when enabled, safe to no-op on error.
}

// preHookUpdateCheck prints an "update available" banner if state.json
// already knows about a newer release, then kicks off a background
// refresh when the last check is older than updateCheckInterval. Opted
// out by --output json, non-TTY stderr, DRIFT_SKIP_UPDATE_CHECK, and for
// commands where a banner would be wrong (help/update/ssh-proxy).
func preHookUpdateCheck(io IO, cli *CLI, command string) {
	if !updateCheckEnabled(cli, io, command) {
		return
	}
	showUpdateBanner(io)
	scheduleUpdateCheck()
}

func updateCheckEnabled(cli *CLI, io IO, command string) bool {
	if cli.Output == "json" {
		return false
	}
	if os.Getenv("DRIFT_SKIP_UPDATE_CHECK") != "" {
		return false
	}
	if !stderrIsTTY(io.Stderr) {
		return false
	}
	switch firstWord(command) {
	case "", "help", "update", "ssh-proxy":
		return false
	}
	return true
}

func firstWord(s string) string {
	if i := strings.Index(s, " "); i >= 0 {
		return s[:i]
	}
	return s
}

// updateBannerLine is the pure-function core of the banner: given the
// running version and the latest known version, it returns what to
// print (or "" for no banner). Kept testable — showUpdateBanner adds
// the I/O and palette.
func updateBannerLine(cur, latest string, p *style.Palette) string {
	cur = strings.TrimPrefix(cur, "v")
	latest = strings.TrimPrefix(latest, "v")
	if cur == "" || cur == "devel" || latest == "" || latest == cur {
		return ""
	}
	return fmt.Sprintf("%s drift %s is available (current %s) — run %s",
		p.Accent("▶"),
		p.Bold(latest),
		cur,
		p.Accent("drift update"),
	)
}

func showUpdateBanner(io IO) {
	st := loadClientState()
	p := style.For(io.Stderr, false)
	if line := updateBannerLine(version.Get().Version, st.LatestVersion, p); line != "" {
		fmt.Fprintln(io.Stderr, line)
	}
}

func scheduleUpdateCheck() {
	st := loadClientState()
	if !st.LastUpdateCheck.IsZero() && time.Since(st.LastUpdateCheck) < updateCheckInterval {
		return
	}
	go backgroundUpdateCheck()
}

// backgroundUpdateCheck uses context.Background on purpose: the parent
// command's ctx may be cancelled the moment the command returns, and we
// want the 10s GitHub call to finish cleanly when the process is
// long-lived (connect, ai). Short-lived commands just let the goroutine
// die with the process — no state.json update, next invocation retries.
func backgroundUpdateCheck() {
	ctx, cancel := context.WithTimeout(context.Background(), updateCheckTimeout)
	defer cancel()
	rel, err := fetchLatestReleaseFn(ctx, updateCheckAPIBase, updateCheckRepo)
	if err != nil {
		return
	}
	st := loadClientState()
	st.LastUpdateCheck = time.Now()
	st.LatestVersion = strings.TrimPrefix(rel.TagName, "v")
	_ = saveClientState(st)
}
