package drift

import (
	"context"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/rpc/client"
)

// deps is the injection surface — tests replace these with fakes so no
// real SSH is invoked. probe must return non-nil result on success or
// error on failure; (nil, nil) is not legal.
type deps struct {
	clientConfigPath func() (string, error)
	probe            func(ctx context.Context, circuit string) (*probeResult, error)
	call             func(ctx context.Context, circuit, method string, params, out any) error
}

func defaultDeps() deps {
	c := client.New()
	return deps{
		clientConfigPath: config.ClientConfigPath,
		probe:            defaultProbe(c),
		call:             c.Call,
	}
}
