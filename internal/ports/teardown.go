package ports

import (
	"context"
	"fmt"
)

// TeardownKart cancels every live forward for one kart and stops its
// ssh master, but leaves state.yaml entries alone. The intent is "the
// connect session ended; the desire to forward these ports is still
// recorded, but right now nothing is bound" — the next `drift connect`
// or `drift ports up` rebinds from the same state entries.
//
// Reads and writes the live cache. State.yaml is not touched. A
// missing master (Check=false) collapses to "just clear the cache
// row" — no error. Per-forward CancelForward errors are surfaced in
// the returned error chain but don't abort the rest of the teardown
// (we still try to stop the master so a partially-working session
// doesn't strand a half-bound master).
func TeardownKart(ctx context.Context, driver Driver, state *State, circuit, kart string) error {
	host := SSHHost(circuit, kart)
	livePathStr, err := livePath()
	if err != nil {
		return err
	}
	live, err := loadLiveCache(livePathStr)
	if err != nil {
		return err
	}

	alive, err := driver.Check(ctx, host)
	if err != nil {
		return fmt.Errorf("ports: teardown %s/%s: check: %w", circuit, kart, err)
	}
	if !alive {
		// Master gone (ControlPersist expired, manual `ssh -O exit`,
		// crashed). Nothing to cancel; just clear the cache row so a
		// later reconcile starts from a clean slate.
		live.set(host, nil)
		return saveLiveCache(livePathStr, live)
	}

	var firstErr error
	for _, p := range live.get(host) {
		if err := driver.CancelForward(ctx, host, p.Local, p.Remote); err != nil && firstErr == nil {
			firstErr = fmt.Errorf("ports: teardown %s/%s: cancel %d→%d: %w", circuit, kart, p.Local, p.Remote, err)
		}
	}
	if err := driver.StopMaster(ctx, host); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("ports: teardown %s/%s: stop master: %w", circuit, kart, err)
	}
	live.set(host, nil)
	if err := saveLiveCache(livePathStr, live); err != nil && firstErr == nil {
		firstErr = err
	}
	return firstErr
}
