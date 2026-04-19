// Package errfmt is the shared human-CLI error formatter: header line,
// then indented sorted `key: value` context from rpcerr.Error.Data. Every
// drift/lakitu command reports failures through [Emit].
package errfmt

import (
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// Emit returns the exit code the caller should propagate. Behavior:
//   - *rpcerr.Error (directly or via errors.As): rendered with type + data
//     lines, returns the rpcerr's Code.
//   - Any other error: rendered as `error: <err.Error()>`, returns CodeInternal.
//   - nil: returns CodeOK without writing.
//
// Styling auto-detects from the writer: a real TTY *os.File gets color,
// bytes.Buffer / pipes / `NO_COLOR=1` fall back to plain text.
//
// Never panics; I/O failures are swallowed (process is already exiting).
func Emit(w io.Writer, err error) int {
	if err == nil {
		return int(rpcerr.CodeOK)
	}
	p := style.For(w, false)
	var re *rpcerr.Error
	if errors.As(err, &re) && re != nil {
		fmt.Fprintf(w, "%s %s\n", p.Error("error:"), re.Message)
		if re.Type != "" {
			fmt.Fprintf(w, "  %s %s\n", p.Dim("type:"), re.Type)
		}
		var devpodTail string
		keys := make([]string, 0, len(re.Data))
		for k, v := range re.Data {
			if k == rpcerr.DataKeyDevpodStderr {
				if s, ok := v.(string); ok {
					devpodTail = s
				}
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "  %s %v\n", p.Dim(k+":"), re.Data[k])
		}
		if devpodTail != "" {
			writeDevpodTail(w, p, devpodTail)
		}
		return int(re.Code)
	}
	fmt.Fprintf(w, "%s %s\n", p.Error("error:"), err.Error())
	return int(rpcerr.CodeInternal)
}

// writeDevpodTail renders the captured devpod stderr as an indented dim
// block, ANSI stripped. Blank trailing lines are dropped so the block sits
// flush against the next piece of output.
func writeDevpodTail(w io.Writer, p *style.Palette, tail string) {
	cleaned := strings.TrimRight(style.StripANSI(tail), "\n")
	if cleaned == "" {
		return
	}
	fmt.Fprintln(w, p.Dim("  devpod output:"))
	for _, line := range strings.Split(cleaned, "\n") {
		fmt.Fprintln(w, p.Dim("    "+line))
	}
}
