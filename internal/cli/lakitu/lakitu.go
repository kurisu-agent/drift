// Package lakitu contains the Kong CLI definition for the lakitu server binary.
//
// Scaffolding covers `version` and a stub `rpc` that reads one JSON-RPC
// request from stdin and returns method_not_found. Handlers land as they
// are implemented.
package lakitu

import (
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/alecthomas/kong"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/version"
	"github.com/kurisu-agent/drift/internal/wire"
)

// CLI is the root argument parser for lakitu.
type CLI struct {
	Debug bool `help:"Verbose output." env:"LAKITU_DEBUG"`

	Version versionCmd `cmd:"" help:"Print lakitu version."`
	RPC     rpcCmd     `cmd:"" name:"rpc" help:"Read one JSON-RPC 2.0 request from stdin and write a response to stdout."`
}

type versionCmd struct {
	Output string `enum:"text,json" default:"text" help:"Output format."`
}

type rpcCmd struct{}

// IO bundles the stdio streams.
type IO struct {
	Stdout io.Writer
	Stderr io.Writer
	Stdin  io.Reader
}

// Run parses argv and dispatches. Returns the process exit code.
func Run(ctx context.Context, argv []string, io IO) int {
	var cli CLI
	parser, err := kong.New(&cli,
		kong.Name("lakitu"),
		kong.Description("lakitu — circuit-side server for drift."),
		kong.Writers(io.Stdout, io.Stderr),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		fmt.Fprintf(io.Stderr, "lakitu: %v\n", err)
		return 1
	}
	kctx, err := parser.Parse(argv)
	if err != nil {
		fmt.Fprintf(io.Stderr, "lakitu: %v\n", err)
		return 2
	}
	switch kctx.Command() {
	case "version":
		return runVersion(io, cli.Version)
	case "rpc":
		return runRPC(ctx, io)
	default:
		fmt.Fprintf(io.Stderr, "lakitu: unknown command %q\n", kctx.Command())
		return 2
	}
}

func runVersion(io IO, cmd versionCmd) int {
	info := version.Get()
	switch cmd.Output {
	case "json":
		buf, err := json.Marshal(info)
		if err != nil {
			fmt.Fprintf(io.Stderr, "lakitu: %v\n", err)
			return 1
		}
		fmt.Fprintln(io.Stdout, string(buf))
	default:
		fmt.Fprintf(io.Stdout, "lakitu %s\n", info.Version)
	}
	return 0
}

// runRPC is the one-shot dispatch entry point. It honors PLAN.md's stdout
// invariant: only the JSON-RPC response (or nothing on a hard crash) ever
// goes to stdout.
func runRPC(_ context.Context, io IO) int {
	req, err := wire.DecodeRequest(io.Stdin)
	if err != nil {
		// Parse error — JSON-RPC 2.0 defines code -32700. drift exit
		// policy: the SSH channel still returns 0 because we delivered a
		// response, but the embedded code maps to CodeInternal (1).
		writeError(io, nil, rpcerr.New(rpcerr.CodeInternal, "parse_error", "parse error: %v", err))
		return 0
	}
	// MVP: no methods wired yet — every request resolves to
	// method_not_found so the transport layer is still exercisable.
	e := rpcerr.New(rpcerr.CodeUserError, "method_not_found", "method %q not implemented", req.Method).
		With("method", req.Method)
	writeError(io, req.ID, e)
	return 0
}

func writeError(io IO, id json.RawMessage, e *rpcerr.Error) {
	envelope := &wire.Response{
		JSONRPC: wire.Version,
		ID:      id,
	}
	buf, _ := e.MarshalJSON()
	var we wire.Error
	_ = json.Unmarshal(buf, &we)
	envelope.Error = &we
	_ = wire.EncodeResponse(io.Stdout, envelope)
}
