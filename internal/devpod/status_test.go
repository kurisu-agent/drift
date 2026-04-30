package devpod_test

import (
	"testing"

	"github.com/kurisu-agent/drift/internal/devpod"
)

func TestFromDockerStateMapsLifecycle(t *testing.T) {
	t.Parallel()
	cases := map[string]devpod.Status{
		"running":    devpod.StatusRunning,
		"Running":    devpod.StatusRunning,
		"exited":     devpod.StatusStopped,
		"created":    devpod.StatusStopped,
		"paused":     devpod.StatusStopped,
		"dead":       devpod.StatusStopped,
		"":           devpod.StatusStopped,
		"restarting": devpod.StatusBusy,
		"removing":   devpod.StatusBusy,
		"garbled":    devpod.StatusError,
	}
	for in, want := range cases {
		t.Run(in+"→"+string(want), func(t *testing.T) {
			t.Parallel()
			if got := devpod.FromDockerState(in); got != want {
				t.Errorf("FromDockerState(%q) = %q, want %q", in, got, want)
			}
		})
	}
}
