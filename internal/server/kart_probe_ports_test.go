package server

import (
	"reflect"
	"testing"

	"github.com/kurisu-agent/drift/internal/wire"
)

func TestParseSSListeners(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want []wire.ProbeListener
	}{
		{
			name: "with process info",
			in: `LISTEN 0 511  127.0.0.1:3000 0.0.0.0:* users:(("node",pid=1234,fd=18))
LISTEN 0 128  [::]:5173      [::]:*    users:(("vite",pid=2345,fd=20))`,
			want: []wire.ProbeListener{
				{Port: 3000, Process: "node"},
				{Port: 5173, Process: "vite"},
			},
		},
		{
			name: "missing users column (other-user listener)",
			in:   `LISTEN 0 4096 0.0.0.0:22 0.0.0.0:*`,
			want: []wire.ProbeListener{{Port: 22}},
		},
		{
			name: "ipv4 + ipv6 collapse on same port",
			in: `LISTEN 0 4096 0.0.0.0:22 0.0.0.0:* users:(("sshd",pid=1,fd=3))
LISTEN 0 4096 [::]:22    [::]:*    users:(("sshd",pid=1,fd=4))`,
			want: []wire.ProbeListener{{Port: 22, Process: "sshd"}},
		},
		{
			name: "garbage rows skipped",
			in: `something else
LISTEN 0 511 127.0.0.1:3000 0.0.0.0:* users:(("node",pid=1,fd=2))
not a row`,
			want: []wire.ProbeListener{{Port: 3000, Process: "node"}},
		},
		{
			name: "out-of-range port skipped",
			in: `LISTEN 0 511 127.0.0.1:99999 0.0.0.0:* users:(("x",pid=1,fd=2))
LISTEN 0 511 127.0.0.1:3000  0.0.0.0:* users:(("node",pid=1,fd=2))`,
			want: []wire.ProbeListener{{Port: 3000, Process: "node"}},
		},
		{
			name: "multi-process collapse to first name",
			in:   `LISTEN 0 511 127.0.0.1:80 0.0.0.0:* users:(("nginx",pid=1,fd=3),("nginx",pid=2,fd=4))`,
			want: []wire.ProbeListener{{Port: 80, Process: "nginx"}},
		},
		{
			name: "empty",
			in:   ``,
			want: nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := parseSSListeners([]byte(c.in))
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %#v, want %#v", got, c.want)
			}
		})
	}
}
