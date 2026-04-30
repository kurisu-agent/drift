package ports

import (
	"errors"
	"fmt"
)

// AddResult tells the caller what reconcile / the CLI should print after
// a successful Add. Remapped is non-zero only when conflict resolution
// pushed Local off the originally requested port.
type AddResult struct {
	Forward  Forward
	Remapped bool // true when Forward.Local != requested local
	NoOp     bool // true when an identical entry was already present
}

// AddForward is the in-memory mutation behind `drift ports add`. It picks
// a free local port if needed, records remapped_from on conflict, and
// returns the resulting Forward. Caller saves the State.
//
// User-explicit calls pass requestedLocal = 0 to mean "match remote";
// the devcontainer passthrough passes the spec port directly. Source
// classifies the origin so reconcile / list / TUI can render it.
func AddForward(
	state *State,
	prober LocalProber,
	circuit, kart string,
	remote, requestedLocal int,
	source Source,
) (AddResult, error) {
	if state == nil {
		return AddResult{}, errors.New("ports: nil state")
	}
	if remote < 1 || remote > 65535 {
		return AddResult{}, fmt.Errorf("ports: remote port out of range: %d", remote)
	}
	if requestedLocal == 0 {
		requestedLocal = remote
	}
	if requestedLocal < 1 || requestedLocal > 65535 {
		return AddResult{}, fmt.Errorf("ports: local port out of range: %d", requestedLocal)
	}

	fwds := state.Get(circuit, kart)

	// Idempotent path: if an entry already targets `remote`, leave it
	// alone — explicit user remaps survive a later devcontainer
	// passthrough that lists the same remote port. Reconcile will
	// happily run with the existing Local.
	if i := Find(fwds, remote); i >= 0 {
		return AddResult{Forward: fwds[i], NoOp: true}, nil
	}

	// Avoid colliding with an entry that already binds requestedLocal
	// for *another* kart. PortsTaken plus a live local probe is enough.
	taken := state.PortsTaken()
	// Don't count ourselves: we just confirmed no entry with remote
	// matches, but a different remote on this kart could be holding the
	// requested local. PortsTaken caught that already.

	var (
		chosen   int
		remapped bool
	)
	if !taken[requestedLocal] && prober.IsFree(requestedLocal) {
		chosen = requestedLocal
	} else {
		picked, err := PickFreePort(prober, requestedLocal+1, taken)
		if err != nil {
			return AddResult{}, err
		}
		chosen = picked
		remapped = true
	}

	f := Forward{
		Local:  chosen,
		Remote: remote,
		Source: source,
	}
	if remapped {
		f.RemappedFrom = requestedLocal
	}
	state.Put(circuit, kart, append(fwds, f))
	return AddResult{Forward: f, Remapped: remapped}, nil
}

// RemoveForward drops the entry whose Remote (or, if remote = 0, Local)
// matches `port` for the given kart. Returns the removed forward.
func RemoveForward(state *State, circuit, kart string, port int) (Forward, error) {
	if state == nil {
		return Forward{}, errors.New("ports: nil state")
	}
	fwds := state.Get(circuit, kart)
	i := Find(fwds, port)
	if i < 0 {
		i = FindByLocal(fwds, port)
	}
	if i < 0 {
		return Forward{}, fmt.Errorf("ports: %s/%s: no forward for %d", circuit, kart, port)
	}
	removed := fwds[i]
	fwds = append(fwds[:i], fwds[i+1:]...)
	state.Put(circuit, kart, fwds)
	return removed, nil
}

// RemapForward moves the Local port for an existing Remote. Pass
// newLocal = 0 to clear an earlier remap (snap Local back to Remote).
func RemapForward(
	state *State,
	prober LocalProber,
	circuit, kart string,
	remote, newLocal int,
) (Forward, error) {
	if state == nil {
		return Forward{}, errors.New("ports: nil state")
	}
	fwds := state.Get(circuit, kart)
	i := Find(fwds, remote)
	if i < 0 {
		return Forward{}, fmt.Errorf("ports: %s/%s: no forward for remote %d", circuit, kart, remote)
	}
	if newLocal == 0 {
		newLocal = remote
	}
	if newLocal < 1 || newLocal > 65535 {
		return Forward{}, fmt.Errorf("ports: local port out of range: %d", newLocal)
	}

	// Verify the new local doesn't clash. PortsTaken includes the
	// current entry's Local, so exclude it before checking.
	taken := state.PortsTaken()
	delete(taken, fwds[i].Local)
	if taken[newLocal] {
		return Forward{}, fmt.Errorf("ports: local %d already mapped on this workstation", newLocal)
	}
	if !prober.IsFree(newLocal) {
		return Forward{}, fmt.Errorf("ports: local %d is bound by something else", newLocal)
	}

	fwds[i].Local = newLocal
	if newLocal == remote {
		fwds[i].RemappedFrom = 0
	} else {
		fwds[i].RemappedFrom = remote
	}
	state.Put(circuit, kart, fwds)
	return fwds[i], nil
}

// UnionDevcontainer reconciles the kart's resolved devcontainer
// forwardPorts into state: ports listed in `spec` get added with
// Source=devcontainer if absent; existing entries (any source) are
// preserved as-is so an explicit user remap survives. Devcontainer
// entries no longer in spec are pruned. Explicit/auto entries are
// untouched.
//
// The plan calls this "removing the port from the devcontainer spec on
// the next connect prunes the entry" — but only for devcontainer-source
// entries. Explicit and auto stay until the user removes them.
func UnionDevcontainer(
	state *State,
	prober LocalProber,
	circuit, kart string,
	spec []int,
) ([]AddResult, error) {
	desired := make(map[int]bool, len(spec))
	for _, p := range spec {
		desired[p] = true
	}

	// Prune devcontainer entries that are no longer in the spec.
	fwds := state.Get(circuit, kart)
	kept := fwds[:0]
	for _, f := range fwds {
		if f.Source == SourceDevcontainer && !desired[f.Remote] {
			continue
		}
		kept = append(kept, f)
	}
	state.Put(circuit, kart, kept)

	// Add anything new from the spec.
	var added []AddResult
	for _, p := range spec {
		res, err := AddForward(state, prober, circuit, kart, p, 0, SourceDevcontainer)
		if err != nil {
			return added, err
		}
		if !res.NoOp {
			added = append(added, res)
		}
	}
	return added, nil
}
