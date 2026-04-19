// Package errfmt is the shared human-CLI error formatter: header line,
// then indented sorted `key: value` context from rpcerr.Error.Data. Every
// drift/lakitu command reports failures through [Emit].
package errfmt

import (
	"errors"
	"fmt"
	"io"
	"sort"

	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// Emit returns the exit code the caller should propagate. Behavior:
//   - *rpcerr.Error (directly or via errors.As): rendered with type + data
//     lines, returns the rpcerr's Code.
//   - Any other error: rendered as `error: <err.Error()>`, returns CodeInternal.
//   - nil: returns CodeOK without writing.
//
// Never panics; I/O failures are swallowed (process is already exiting).
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
