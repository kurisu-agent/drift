package ports

import (
	"context"
	"fmt"
	"sort"
)

// ReconcileOptions narrows reconcile's scope. Zero value reconciles every
// kart in the state file plus every kart with a stale master in the live
// cache (so removed entries get cleaned up).
type ReconcileOptions struct {
	// OnlyKart, when set, restricts reconcile to a single kart key
	// ("<circuit>/<kart>"). Other karts in the state / live cache are
	// untouched. Used by `drift connect`'s pre-exec hook.
	OnlyKart string
}

// ReconcileReport names the changes reconcile made on this invocation.
// The CLI prints a one-line summary from it; the TUI displays it inline.
type ReconcileReport struct {
	// Changes is one human-readable line per delta, in the order applied.
	Changes []string
	// Errors collects per-step failures. Reconcile is best-effort: a
	// failed AddForward or StartMaster is reported but does not abort
	// processing of other karts.
	Errors []error
}

// Reconcile walks `state` (the desired forwards) and `live` (what we
// believe is installed) and uses `driver` to converge them. It returns
// an updated live-cache that the caller persists.
func Reconcile(
	ctx context.Context,
	state *State,
	live *liveCache,
	driver Driver,
	opts ReconcileOptions,
) (*ReconcileReport, *liveCache, error) {
	if state == nil {
		state = &State{Version: CurrentVersion}
	}
	if live == nil {
		live = &liveCache{Hosts: map[string]liveHost{}}
	}
	report := &ReconcileReport{}
	out := &liveCache{Hosts: map[string]liveHost{}}
	for h, lh := range live.Hosts {
		out.Hosts[h] = lh
	}

	// Build the set of (circuit, kart) keys we'll touch this pass:
	// every key in state, plus every host the cache thinks has live
	// forwards (so removals get cleaned up). Filter by OnlyKart.
	keys := map[string]bool{}
	for k := range state.Forwards {
		keys[k] = true
	}
	for h := range live.Hosts {
		if c, k, ok := splitSSHHost(h); ok {
			keys[KartKey(c, k)] = true
		}
	}
	keyList := make([]string, 0, len(keys))
	for k := range keys {
		if opts.OnlyKart != "" && k != opts.OnlyKart {
			continue
		}
		keyList = append(keyList, k)
	}
	sort.Strings(keyList)

	for _, key := range keyList {
		circuit, kart, ok := SplitKartKey(key)
		if !ok {
			report.Errors = append(report.Errors, fmt.Errorf("ports: skip malformed key %q", key))
			continue
		}
		host := SSHHost(circuit, kart)
		desired := livePairsFromForwards(state.Forwards[key])
		if err := reconcileOne(ctx, host, key, desired, out, driver, report); err != nil {
			report.Errors = append(report.Errors, err)
		}
	}

	return report, out, nil
}

func reconcileOne(
	ctx context.Context,
	host, key string,
	desired []livePair,
	live *liveCache,
	driver Driver,
	report *ReconcileReport,
) error {
	alive, err := driver.Check(ctx, host)
	if err != nil {
		return err
	}

	// No desired forwards: tear the master down if it's up, and drop the
	// host from the cache. ControlPersist's 10m would clean it up
	// eventually, but we'd rather not strand idle masters when the user
	// just typed `drift ports rm`.
	if len(desired) == 0 {
		if alive {
			if err := driver.StopMaster(ctx, host); err != nil {
				return err
			}
			report.Changes = append(report.Changes, fmt.Sprintf("%s: master stopped", key))
		}
		live.set(host, nil)
		return nil
	}

	// Master gone but we wanted forwards: restart from a clean slate.
	// Wiping the cache slot here is what makes "master died between
	// invocations" the same code path as "master never existed".
	current := live.get(host)
	if !alive {
		if err := driver.StartMaster(ctx, host); err != nil {
			return err
		}
		report.Changes = append(report.Changes, fmt.Sprintf("%s: master started", key))
		current = nil
	}

	add, cancel := diffPairs(current, desired)

	// Cancel before add so a remap (cancel old Local, add new Local) doesn't
	// briefly try to bind both at once.
	for _, p := range cancel {
		if err := driver.CancelForward(ctx, host, p.Local, p.Remote); err != nil {
			report.Errors = append(report.Errors, err)
			continue
		}
		report.Changes = append(report.Changes, fmt.Sprintf("%s: -%d→%d", key, p.Local, p.Remote))
	}

	installed := make([]livePair, 0, len(desired))
	// Carry forward anything we did not cancel.
	for _, p := range current {
		if !pairIn(cancel, p) {
			installed = append(installed, p)
		}
	}
	for _, p := range add {
		if err := driver.AddForward(ctx, host, p.Local, p.Remote); err != nil {
			report.Errors = append(report.Errors, err)
			continue
		}
		installed = append(installed, p)
		report.Changes = append(report.Changes, fmt.Sprintf("%s: +%d→%d", key, p.Local, p.Remote))
	}
	live.set(host, installed)
	return nil
}

func pairIn(set []livePair, p livePair) bool {
	for _, q := range set {
		if q == p {
			return true
		}
	}
	return false
}

// splitSSHHost reverses SSHHost: "drift.<circuit>.<kart>" → (circuit, kart).
// Returns ok=false if the host is not in drift's namespace.
func splitSSHHost(h string) (circuit, kart string, ok bool) {
	const prefix = "drift."
	if len(h) <= len(prefix) || h[:len(prefix)] != prefix {
		return "", "", false
	}
	rest := h[len(prefix):]
	for i := 0; i < len(rest); i++ {
		if rest[i] == '.' {
			if i == 0 || i == len(rest)-1 {
				return "", "", false
			}
			return rest[:i], rest[i+1:], true
		}
	}
	return "", "", false
}

// LoadAndReconcile is the convenience wrapper drift's CLI uses: read the
// state file, read the cache, reconcile, write the cache back. The
// state file itself is unchanged — reconcile reads only.
func LoadAndReconcile(
	ctx context.Context,
	statePath string,
	driver Driver,
	opts ReconcileOptions,
) (*ReconcileReport, error) {
	state, err := Load(statePath)
	if err != nil {
		return nil, err
	}
	livePathStr, err := livePath()
	if err != nil {
		return nil, err
	}
	live, err := loadLiveCache(livePathStr)
	if err != nil {
		return nil, err
	}
	report, updated, err := Reconcile(ctx, state, live, driver, opts)
	if err != nil {
		return report, err
	}
	if err := saveLiveCache(livePathStr, updated); err != nil {
		return report, err
	}
	return report, nil
}
