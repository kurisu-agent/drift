package drift

import (
	"reflect"
	"testing"

	"github.com/kurisu-agent/drift/internal/wire"
)

func TestMergeProbeCandidates(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		listeners []wire.ProbeListener
		spec      []int
		exclude   map[int]bool
		want      []probeCandidate
	}{
		{
			name:      "listeners only",
			listeners: []wire.ProbeListener{{Port: 3000, Process: "node"}, {Port: 5173, Process: "vite"}},
			want: []probeCandidate{
				{Port: 3000, Process: "node"},
				{Port: 5173, Process: "vite"},
			},
		},
		{
			name: "devcontainer only",
			spec: []int{3000, 5432},
			want: []probeCandidate{
				{Port: 3000, Devcontainer: true},
				{Port: 5432, Devcontainer: true},
			},
		},
		{
			name:      "overlap keeps process and tags devcontainer",
			listeners: []wire.ProbeListener{{Port: 3000, Process: "node"}},
			spec:      []int{3000, 5432},
			want: []probeCandidate{
				{Port: 3000, Process: "node", Devcontainer: true},
				{Port: 5432, Devcontainer: true},
			},
		},
		{
			name:      "exclude drops from both sources",
			listeners: []wire.ProbeListener{{Port: 22, Process: "sshd"}, {Port: 3000, Process: "node"}},
			spec:      []int{22, 5432},
			exclude:   map[int]bool{22: true},
			want: []probeCandidate{
				{Port: 3000, Process: "node"},
				{Port: 5432, Devcontainer: true},
			},
		},
		{
			name:      "empty inputs",
			listeners: nil,
			spec:      nil,
			want:      nil,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			got := mergeProbeCandidates(c.listeners, c.spec, c.exclude)
			if !reflect.DeepEqual(got, c.want) {
				t.Errorf("got %#v, want %#v", got, c.want)
			}
		})
	}
}
