package server

import (
	"bufio"
	"context"
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/kart"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// kartProbePortsHandler enumerates ports the kart wants forwarded by
// combining two sources:
//
//  1. Live listeners inside the kart, via `ss -tlnpH` over devpod ssh.
//     Captures whatever's actually bound right now (vite, postgres,
//     ad-hoc test servers).
//  2. Static `forwardPorts` from the kart's devcontainer.json. Captures
//     the kart authors' intent — these are useful even before the kart
//     is up, so users can pre-select them.
//
// The probe lives server-side because lakitu already speaks devpod,
// owns DEVPOD_HOME, and knows where each kart's workspace is checked
// out (devcontainer.json sits inside the cloned content, not at a
// path the workstation can guess). The output format (`ss -tlnH` rows
// plus hujson devcontainer parsing) is implementation detail; the
// client never has to know.
//
// Both sources are best-effort: if `ss` fails (kart not running,
// devpod not available, ss missing) we still return the devcontainer
// ports. The handler only escalates to a hard error when the kart
// itself can't be found in the garage.
//
// Returned ports are raw — port 22 and already-configured forwards are
// filtered client-side because that's UX policy, not server truth.
func (d KartDeps) kartProbePortsHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p wire.KartProbePortsParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if err := d.requireDevpod(); err != nil {
		return nil, err
	}
	_, ok, err := d.readKartConfig(p.Name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, rpcerr.NotFound(rpcerr.TypeKartNotFound,
			"kart %q not found", p.Name).With("kart", p.Name)
	}

	// Static side: parse devcontainer.json from disk. This works whether
	// or not the kart's container is up.
	devcontainerPorts := kart.ProbeForwardPorts(p.Name)

	// Live side: `-p` adds the `users:(("name",pid=...,fd=...))` column
	// so the picker can show "vite (:5173)" instead of just ":5173". The
	// column is empty for listeners owned by another user — that's fine;
	// the parser just returns Process="" in that case. A failure here is
	// soft: the kart may be stopped, or `ss` may not be in the image. We
	// still return the devcontainer ports so the picker has something.
	var listeners []wire.ProbeListener
	out, err := d.Devpod.SSH(ctx, devpod.SSHOpts{
		Name:            p.Name,
		Command:         "ss -tlnpH",
		NoStartServices: true,
	})
	if err == nil {
		listeners = parseSSListeners(out)
	}
	return wire.KartProbePortsResult{
		Listeners:         listeners,
		DevcontainerPorts: devcontainerPorts,
	}, nil
}

// parseSSListeners walks `ss -tlnpH` rows and returns the deduplicated
// set of (port, process) pairs sorted by port. ss output:
//
//	LISTEN 0 4096 0.0.0.0:22     0.0.0.0:*
//	LISTEN 0 511  127.0.0.1:3000 0.0.0.0:* users:(("node",pid=1234,fd=18))
//	LISTEN 0 128  [::]:5173      [::]:*    users:(("vite",pid=2345,fd=20))
//
// Column 4 is the local-address; trailing fields hold the optional
// `users:((...))` block. The first quoted name inside users:(()) is
// the process name; we drop pid/fd. Multiple users entries on the
// same port collapse to the first name. Listeners owned by another
// user inside the kart show no users:(()) column at all and get
// Process="".
func parseSSListeners(stdout []byte) []wire.ProbeListener {
	seen := make(map[int]bool)
	var out []wire.ProbeListener
	scanner := bufio.NewScanner(strings.NewReader(string(stdout)))
	for scanner.Scan() {
		port, proc, ok := parseSSListenerLine(scanner.Text())
		if !ok || seen[port] {
			continue
		}
		seen[port] = true
		out = append(out, wire.ProbeListener{Port: port, Process: proc})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Port < out[j].Port })
	return out
}

func parseSSListenerLine(line string) (port int, process string, ok bool) {
	fields := strings.Fields(line)
	if len(fields) < 5 {
		return 0, "", false
	}
	addr := fields[3]
	idx := strings.LastIndexByte(addr, ':')
	if idx < 0 {
		return 0, "", false
	}
	port, err := strconv.Atoi(addr[idx+1:])
	if err != nil || port < 1 || port > 65535 {
		return 0, "", false
	}
	for _, f := range fields[5:] {
		if name, found := parseUsersProcess(f); found {
			return port, name, true
		}
	}
	return port, "", true
}

// parseUsersProcess pulls the process name out of an `ss -p` users
// column: `users:(("vite",pid=2345,fd=20),...)`. Returns the FIRST
// quoted name; multi-process listeners are rare in dev containers and
// "vite" is more useful in the picker than "vite,vite,vite".
func parseUsersProcess(field string) (string, bool) {
	const prefix = `users:(("`
	if !strings.HasPrefix(field, prefix) {
		return "", false
	}
	rest := field[len(prefix):]
	end := strings.IndexByte(rest, '"')
	if end <= 0 {
		return "", false
	}
	return rest[:end], true
}

// RegisterKartProbePorts wires kart.probe_ports into the registry.
// Separate registration mirrors RegisterKartConnect — keeps tests from
// having to compose every kart RPC just to exercise this one.
func RegisterKartProbePorts(reg *rpc.Registry, d KartDeps) {
	reg.Register(wire.MethodKartProbePorts, d.kartProbePortsHandler)
}
