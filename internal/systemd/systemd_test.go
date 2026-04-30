package systemd_test

import (
	"context"
	"errors"
	"testing"

	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/systemd"
)

// stubRunner records the last argv and returns the configured result so we
// can assert on the systemctl invocation without spawning a real process.
type stubRunner struct {
	args   []string
	result driftexec.Result
	err    error
}

func (s *stubRunner) Run(ctx context.Context, cmd driftexec.Cmd) (driftexec.Result, error) {
	s.args = append([]string{cmd.Name}, cmd.Args...)
	return s.result, s.err
}

func TestUnitFor(t *testing.T) {
	if got := systemd.UnitFor("alpha"); got != "lakitu-kart@alpha.service" {
		t.Errorf("UnitFor = %q, want lakitu-kart@alpha.service", got)
	}
}

func TestEnableBuildsCorrectArgv(t *testing.T) {
	r := &stubRunner{}
	c := &systemd.Client{Runner: r}
	if err := c.Enable(context.Background(), "alpha"); err != nil {
		t.Fatalf("Enable returned %v", err)
	}
	want := []string{"systemctl", "--user", "enable", "--now", "lakitu-kart@alpha.service"}
	if !equal(r.args, want) {
		t.Errorf("argv = %v, want %v", r.args, want)
	}
}

func TestDisableBuildsCorrectArgv(t *testing.T) {
	r := &stubRunner{}
	c := &systemd.Client{Runner: r}
	if err := c.Disable(context.Background(), "alpha"); err != nil {
		t.Fatalf("Disable returned %v", err)
	}
	want := []string{"systemctl", "--user", "disable", "--now", "lakitu-kart@alpha.service"}
	if !equal(r.args, want) {
		t.Errorf("argv = %v, want %v", r.args, want)
	}
}

func TestIsEnabledReadsStdoutToken(t *testing.T) {
	cases := []struct {
		stdout string
		want   bool
	}{
		{"enabled\n", true},
		{"enabled-runtime\n", true},
		{"disabled\n", false},
		{"masked\n", false},
		{"static\n", false},
		{"not-found\n", false},
	}
	for _, tc := range cases {
		t.Run(tc.stdout, func(t *testing.T) {
			r := &stubRunner{result: driftexec.Result{Stdout: []byte(tc.stdout)}}
			c := &systemd.Client{Runner: r}
			got, err := c.IsEnabled(context.Background(), "alpha")
			if err != nil {
				t.Fatalf("IsEnabled returned %v", err)
			}
			if got != tc.want {
				t.Errorf("IsEnabled = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestEnableMapsDenialStderrToDenialError(t *testing.T) {
	r := &stubRunner{
		err: &driftexec.Error{
			Name:            "systemctl",
			ExitCode:        1,
			Stderr:          []byte("Failed to connect to user bus: No such file or directory\n"),
			FirstStderrLine: "Failed to connect to user bus: No such file or directory",
		},
	}
	c := &systemd.Client{Runner: r}
	err := c.Enable(context.Background(), "alpha")
	if err == nil {
		t.Fatal("Enable returned nil err, want DenialError")
	}
	var de *systemd.DenialError
	if !errors.As(err, &de) {
		t.Fatalf("err is not *DenialError: %T", err)
	}
	if de.Kart != "alpha" || de.Op != "enable" {
		t.Errorf("DenialError = %+v, want kart=alpha op=enable", de)
	}
}

func TestEnableGenericExecErrorNotDenial(t *testing.T) {
	r := &stubRunner{
		err: &driftexec.Error{
			Name:            "systemctl",
			ExitCode:        1,
			Stderr:          []byte("Unit lakitu-kart@alpha.service not found.\n"),
			FirstStderrLine: "Unit lakitu-kart@alpha.service not found.",
		},
	}
	c := &systemd.Client{Runner: r}
	err := c.Enable(context.Background(), "alpha")
	if err == nil {
		t.Fatal("Enable returned nil err")
	}
	var de *systemd.DenialError
	if errors.As(err, &de) {
		t.Error("err should not be classified as DenialError")
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
