package ports

import (
	"fmt"
	"net"
	"strconv"
)

// LocalProber tells reconcile / add whether a workstation port is free to
// bind on 127.0.0.1. The production binding is DefaultLocalProber, which
// performs a non-blocking bind-and-close. Tests substitute a fake.
type LocalProber interface {
	IsFree(port int) bool
}

// LocalProberFunc adapts a plain function to LocalProber.
type LocalProberFunc func(port int) bool

func (f LocalProberFunc) IsFree(port int) bool { return f(port) }

// DefaultLocalProber probes 127.0.0.1:<port> via a non-blocking TCP listen.
// We deliberately bind the loopback (not 0.0.0.0) because that is exactly
// what `ssh -L localPort:localhost:remotePort` will bind, so any false
// positive from probing the wildcard would be misleading.
var DefaultLocalProber LocalProber = LocalProberFunc(func(port int) bool {
	addr := net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
	l, err := net.Listen("tcp", addr)
	if err != nil {
		return false
	}
	_ = l.Close()
	return true
})

// PickFreePort returns the first port at or after start that the prober
// reports free, skipping anything in `taken`. taken handles the "the
// state file already plans to use this port for another kart" case so
// the workstation doesn't double-book itself.
func PickFreePort(p LocalProber, start int, taken map[int]bool) (int, error) {
	if start < 1 || start > 65535 {
		return 0, fmt.Errorf("ports: start out of range: %d", start)
	}
	for port := start; port <= 65535; port++ {
		if taken[port] {
			continue
		}
		if p.IsFree(port) {
			return port, nil
		}
	}
	return 0, fmt.Errorf("ports: no free port at or after %d", start)
}

// PortsTaken collects every Local across the whole state file. Callers
// pass it to PickFreePort to avoid colliding with another kart's mapping.
func (s *State) PortsTaken() map[int]bool {
	taken := make(map[int]bool)
	for _, fwds := range s.Forwards {
		for _, f := range fwds {
			taken[f.Local] = true
		}
	}
	return taken
}
