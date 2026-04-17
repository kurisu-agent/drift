// Package errfmt is the shared formatter for CLI errors on the human path.
//
// plans/PLAN.md § "stderr format (human CLI path)" fixes the shape:
//
//	error: <message>
//	{<single-line JSON of the error object>}
//
// and requires the process exit code to mirror the error's Code. [Emit] is
// the single entry point that realises that contract. Every drift and lakitu
// CLI command that needs to report a failure routes through it so the format
// stays consistent across binaries and subcommands.
package errfmt

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// Emit writes the standard two-line error representation to w and returns the
// exit code the caller should propagate. Behavior:
//
//   - A *rpcerr.Error (including one reachable via errors.As) is rendered with
//     line 1 = "error: <Message>" and line 2 = the single-line JSON of the
//     error object. The returned exit code is the rpcerr's Code.
//   - Any other error type falls back to line 1 = "error: <err.Error()>"
//     with no JSON line. The returned exit code is rpcerr.CodeInternal (1),
//     which is the documented default for untyped failures.
//   - A nil error is treated as success and returns CodeOK (0) without
//     writing anything.
//
// Emit never panics and never returns early on an I/O error — the process is
// already exiting; best-effort writes are sufficient.
func Emit(w io.Writer, err error) int {
	if err == nil {
		return int(rpcerr.CodeOK)
	}
	var re *rpcerr.Error
	if errors.As(err, &re) && re != nil {
		fmt.Fprintf(w, "error: %s\n", re.Message)
		if buf, mErr := json.Marshal(re); mErr == nil {
			fmt.Fprintln(w, string(buf))
		}
		return int(re.Code)
	}
	fmt.Fprintf(w, "error: %s\n", err.Error())
	return int(rpcerr.CodeInternal)
}
