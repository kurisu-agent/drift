package drift

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"
)

func TestAwaitTunnelReady_dialSucceeds(t *testing.T) {
	t.Parallel()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	port := ln.Addr().(*net.TCPAddr).Port

	done := make(chan error, 1) // never fires
	sshExit, status := awaitTunnelReady(context.Background(), port, done, time.Second)
	if status != nil {
		t.Fatalf("expected ready, got status=%v", status)
	}
	if sshExit != nil {
		t.Errorf("sshExit = %v, want nil on dial-success path", sshExit)
	}
}

func TestAwaitTunnelReady_processExits(t *testing.T) {
	t.Parallel()
	done := make(chan error, 1)
	wantExit := errors.New("ssh exited 255")
	done <- wantExit
	// Use a definitely-closed port (a listener we close immediately) so
	// the dial keeps failing and the function falls through to the
	// tunnelDone branch.
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	sshExit, status := awaitTunnelReady(context.Background(), port, done, time.Second)
	if !errors.Is(status, errTunnelExited) {
		t.Fatalf("status = %v, want errTunnelExited", status)
	}
	if !errors.Is(sshExit, wantExit) {
		t.Errorf("sshExit = %v, want %v passed through from tunnelDone", sshExit, wantExit)
	}
}

func TestAwaitTunnelReady_timeout(t *testing.T) {
	t.Parallel()
	done := make(chan error, 1) // never fires
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	_ = ln.Close()

	sshExit, status := awaitTunnelReady(context.Background(), port, done, 200*time.Millisecond)
	if !errors.Is(status, context.DeadlineExceeded) {
		t.Fatalf("status = %v, want DeadlineExceeded", status)
	}
	if sshExit != nil {
		t.Errorf("sshExit = %v, want nil on timeout path", sshExit)
	}
}

func TestLastNonEmptyLines(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		n    int
		want string
	}{
		{"empty", "", 5, ""},
		{"single", "boom\n", 5, "boom"},
		{
			"trim_to_n",
			"a\nb\nc\nd\ne\n",
			3,
			"c\nd\ne",
		},
		{
			"skip_blanks",
			"a\n\n\nb\n\n",
			5,
			"a\nb",
		},
		{
			"ssh_style",
			"OpenSSH_9.0p1, OpenSSL 3.0.2\ndebug1: Reading configuration data\nkex_exchange_identification: read: Connection reset by peer\n",
			2,
			"debug1: Reading configuration data\nkex_exchange_identification: read: Connection reset by peer",
		},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := lastNonEmptyLines(tc.in, tc.n)
			if got != tc.want {
				t.Errorf("lastNonEmptyLines(%q, %d) = %q, want %q", tc.in, tc.n, got, tc.want)
			}
		})
	}
}
