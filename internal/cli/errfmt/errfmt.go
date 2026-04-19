// Package errfmt is the shared formatter for CLI errors on the human path.
//
// The format is a header line, then indented `key: value` context lines
// drawn from rpcerr.Error's Type and Data fields. [Emit] is the single
// entry point that realises that contract so every drift and lakitu
// command reports failures the same way.
package errfmt

import (
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// Emit writes a human-readable error representation to w and returns the
// exit code the caller should propagate. Behavior:
//
//   - A *rpcerr.Error (including one reachable via errors.As) is rendered as:
//     `error: <Message>` on line 1, then indented `  type: <Type>` and one
//     `  <key>: <value>` line per entry in Data (sorted for determinism).
//     Returns the rpcerr's Code as the exit status.
//   - Any other error type falls back to `error: <err.Error()>` and returns
//     rpcerr.CodeInternal (1), the documented default for untyped failures.
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
		if re.Type != "" {
			fmt.Fprintf(w, "  type: %s\n", re.Type)
		}
		keys := make([]string, 0, len(re.Data))
		for k := range re.Data {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  %s: %v\n", k, re.Data[k])
		}
		return int(re.Code)
	}
	fmt.Fprintf(w, "error: %s\n", err.Error())
	return int(rpcerr.CodeInternal)
}
