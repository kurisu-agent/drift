package ports

import (
	"net"
	"testing"
)

func TestPickFreePort_skipsTaken(t *testing.T) {
	t.Parallel()
	prober := LocalProberFunc(func(int) bool { return true })
	got, err := PickFreePort(prober, 3000, map[int]bool{3000: true, 3001: true})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 3002 {
		t.Errorf("got %d, want 3002", got)
	}
}

func TestPickFreePort_skipsBound(t *testing.T) {
	t.Parallel()
	prober := LocalProberFunc(func(p int) bool { return p != 3000 })
	got, err := PickFreePort(prober, 3000, nil)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if got != 3001 {
		t.Errorf("got %d, want 3001", got)
	}
}

func TestDefaultLocalProber_realBind(t *testing.T) {
	t.Parallel()
	// Bind a real loopback listener and assert the prober reports it busy.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer func() { _ = l.Close() }()
	port := l.Addr().(*net.TCPAddr).Port
	if DefaultLocalProber.IsFree(port) {
		t.Errorf("port %d should be reported busy while a listener holds it", port)
	}
}
