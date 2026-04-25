// Package errfmt is the shared human-CLI error formatter: header line,
// then indented sorted `key: value` context from rpcerr.Error.Data. Every
// drift/lakitu command reports failures through [Emit].
package errfmt

import (
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	"github.com/kurisu-agent/drift/internal/cli/ui"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// keyIndent is the two-space prefix on data lines (`  key: value`);
// blockIndent is the four-space prefix for the lines inside a fenced
// block (devpod stderr/stdout tail).
const (
	keyIndent   = "  "
	blockIndent = "    "
)

// ansiRE matches SGR / CSI escape sequences. Only errfmt needs to scrub
// devpod's own colored output before re-emitting it through our styler.
var ansiRE = regexp.MustCompile(`\x1b\[[0-9;?]*[a-zA-Z]`)

func stripANSI(s string) string { return ansiRE.ReplaceAllString(s, "") }

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
	p := ui.NewTheme(w, false)
	var re *rpcerr.Error
	if errors.As(err, &re) && re != nil {
		fmt.Fprintf(w, "%s %s\n", p.Error("error:"), re.Message)
		if re.Type != "" {
			fmt.Fprintf(w, "%s%s %s\n", keyIndent, p.Dim("type:"), re.Type)
		}
		var devpodStderr, devpodStdout string
		keys := make([]string, 0, len(re.Data))
		for k, v := range re.Data {
			if k == rpcerr.DataKeyDevpodStderr {
				if s, ok := v.(string); ok {
					devpodStderr = s
				}
				continue
			}
			if k == rpcerr.DataKeyDevpodStdout {
				if s, ok := v.(string); ok {
					devpodStdout = s
				}
				continue
			}
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			fmt.Fprintf(w, "%s%s %v\n", keyIndent, p.Dim(k+":"), re.Data[k])
		}
		if devpodStderr != "" {
			writeDevpodTail(w, p, "devpod stderr:", devpodStderr)
		}
		if devpodStdout != "" {
			writeDevpodTail(w, p, "devpod stdout:", devpodStdout)
		}
		return int(re.Code)
	}
	fmt.Fprintf(w, "%s %s\n", p.Error("error:"), err.Error())
	return int(rpcerr.CodeInternal)
}

// writeDevpodTail renders a captured devpod output stream as an indented
// dim block, ANSI stripped. Blank trailing lines are dropped so the block
// sits flush against the next piece of output. label is the header (e.g.
// "devpod stderr:" / "devpod stdout:") so multiple streams can stack.
func writeDevpodTail(w io.Writer, p *ui.Theme, label, tail string) {
	cleaned := strings.TrimRight(stripANSI(tail), "\n")
	if cleaned == "" {
		return
	}
	fmt.Fprintln(w, p.Dim(keyIndent+label))
	for _, line := range strings.Split(cleaned, "\n") {
		fmt.Fprintln(w, p.Dim(blockIndent+line))
	}
}
