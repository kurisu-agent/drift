package ports

import (
	"context"
	"errors"
	"fmt"

	driftexec "github.com/kurisu-agent/drift/internal/exec"
)

// Driver wraps the ssh control-master verbs reconcile drives. Production
// uses sshDriver (via internal/exec.Runner so Termux's linker wrap is
// applied). Tests substitute a fake.
type Driver interface {
	// Check pings the control socket. Returns true iff a master is alive.
	Check(ctx context.Context, sshHost string) (bool, error)
	// StartMaster opens a background master: ssh -M -N -f sshHost.
	StartMaster(ctx context.Context, sshHost string) error
	// StopMaster sends -O exit. A missing master is not an error.
	StopMaster(ctx context.Context, sshHost string) error
	// AddForward installs a new -L localPort:127.0.0.1:remotePort.
	AddForward(ctx context.Context, sshHost string, localPort, remotePort int) error
	// CancelForward removes one previously added with AddForward.
	CancelForward(ctx context.Context, sshHost string, localPort, remotePort int) error
}

// NewSSHDriver builds the production driver. runner is the exec.Runner the
// caller already has (Termux-aware); pass driftexec.DefaultRunner outside
// of tests.
func NewSSHDriver(runner driftexec.Runner) Driver {
	if runner == nil {
		runner = driftexec.DefaultRunner
	}
	return &sshDriver{runner: runner}
}

type sshDriver struct {
	runner driftexec.Runner
}

func (d *sshDriver) Check(ctx context.Context, sshHost string) (bool, error) {
	_, err := d.runner.Run(ctx, driftexec.Cmd{
		Name: "ssh",
		Args: []string{"-O", "check", sshHost},
	})
	if err == nil {
		return true, nil
	}
	var execErr *driftexec.Error
	if errors.As(err, &execErr) {
		// `ssh -O check` exits 255 when no master is running. Treat any
		// non-zero as "not alive" and surface only startup errors.
		return false, nil
	}
	return false, fmt.Errorf("ports: ssh -O check %s: %w", sshHost, err)
}

func (d *sshDriver) StartMaster(ctx context.Context, sshHost string) error {
	// -M master, -N no remote command, -f background after auth, -T no pty.
	// ConnectTimeout caps the auth phase — a wedged ProxyCommand should
	// fail fast rather than stranding reconcile until ssh's default
	// blocking behaviour gives up. ssh handles ControlPath / ControlPersist
	// via ssh_config, which sshconf already wrote.
	_, err := d.runner.Run(ctx, driftexec.Cmd{
		Name: "ssh",
		Args: []string{"-M", "-N", "-f", "-T", "-o", "ConnectTimeout=10", sshHost},
	})
	if err != nil {
		return fmt.Errorf("ports: ssh -M -N -f %s: %w", sshHost, err)
	}
	return nil
}

func (d *sshDriver) StopMaster(ctx context.Context, sshHost string) error {
	_, err := d.runner.Run(ctx, driftexec.Cmd{
		Name: "ssh",
		Args: []string{"-O", "exit", sshHost},
	})
	if err == nil {
		return nil
	}
	var execErr *driftexec.Error
	if errors.As(err, &execErr) {
		// Already gone is fine.
		return nil
	}
	return fmt.Errorf("ports: ssh -O exit %s: %w", sshHost, err)
}

func (d *sshDriver) AddForward(ctx context.Context, sshHost string, localPort, remotePort int) error {
	spec := fmt.Sprintf("%d:127.0.0.1:%d", localPort, remotePort)
	_, err := d.runner.Run(ctx, driftexec.Cmd{
		Name: "ssh",
		Args: []string{"-O", "forward", "-L", spec, sshHost},
	})
	if err != nil {
		return fmt.Errorf("ports: ssh -O forward -L %s %s: %w", spec, sshHost, err)
	}
	return nil
}

func (d *sshDriver) CancelForward(ctx context.Context, sshHost string, localPort, remotePort int) error {
	spec := fmt.Sprintf("%d:127.0.0.1:%d", localPort, remotePort)
	_, err := d.runner.Run(ctx, driftexec.Cmd{
		Name: "ssh",
		Args: []string{"-O", "cancel", "-L", spec, sshHost},
	})
	if err == nil {
		return nil
	}
	var execErr *driftexec.Error
	if errors.As(err, &execErr) {
		// Cancel of a forward that no longer exists is fine — the desired
		// post-state (it's gone) holds either way.
		return nil
	}
	return fmt.Errorf("ports: ssh -O cancel -L %s %s: %w", spec, sshHost, err)
}
