package drift

import (
	"context"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpc/client"
	"github.com/kurisu-agent/drift/internal/wire"
)

// deps is the injection surface — tests replace these with fakes so no
// real SSH is invoked. probe / probeInfo must return non-nil result on
// success or error on failure; (nil, nil) is not legal.
type deps struct {
	clientConfigPath func() (string, error)
	probe            func(ctx context.Context, circuit string) (*probeResult, error)
	// probeInfo is used by `circuit add` before the drift.<name> alias
	// exists on disk — it ssh's directly to sshArgs (e.g. ["alice@host"]
	// or ["-p", "2222", "alice@host"]).
	probeInfo func(ctx context.Context, sshArgs []string) (*wire.ServerInfo, error)
	call      func(ctx context.Context, circuit, method string, params, out any) error
}

func defaultDeps() deps {
	c := client.New()
	return deps{
		clientConfigPath: config.ClientConfigPath,
		probe:            defaultProbe(c),
		probeInfo:        defaultProbeInfo(),
		call:             defaultCall(c),
	}
}

func defaultProbeInfo() func(ctx context.Context, sshArgs []string) (*wire.ServerInfo, error) {
	return func(ctx context.Context, sshArgs []string) (*wire.ServerInfo, error) {
		c := &client.Client{Transport: client.SSHTransportArgs(sshArgs)}
		var info wire.ServerInfo
		if err := c.Call(ctx, "", wire.MethodServerInfo, struct{}{}, &info); err != nil {
			return nil, err
		}
		return &info, nil
	}
}
