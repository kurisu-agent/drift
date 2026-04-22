package run

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

// Render expands an entry's command template with the caller-supplied
// positional args. Available data:
//   - .Arg N — the Nth positional arg (0-indexed). Missing indices
//     render as "" rather than erroring, so `drift run ping` with no
//     host produces whatever ping itself does on empty input.
//   - .Args — all args joined by a single space, each shell-quoted.
//
// Template funcs:
//   - shq — POSIX-shell single-quote a string.
func Render(cmd string, args []string) (string, error) {
	t, err := template.New("run").
		Funcs(template.FuncMap{"shq": shellQuote}).
		Option("missingkey=zero").
		Parse(cmd)
	if err != nil {
		return "", fmt.Errorf("run: parse template: %w", err)
	}
	data := templateData{args: args}
	var buf bytes.Buffer
	if err := t.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("run: execute template: %w", err)
	}
	return buf.String(), nil
}

type templateData struct {
	args []string
}

// Arg returns the Nth positional arg or empty string. Template callers use
// `{{ .Arg 0 | shq }}` when they need the value safely embedded in a shell
// snippet.
func (t templateData) Arg(i int) string {
	if i < 0 || i >= len(t.args) {
		return ""
	}
	return t.args[i]
}

// Args joins every arg with single spaces, each shell-quoted. Useful for
// passing a trailing varargs list through to the remote command.
func (t templateData) Args() string {
	parts := make([]string, len(t.args))
	for i, a := range t.args {
		parts[i] = shellQuote(a)
	}
	return strings.Join(parts, " ")
}

// shellQuote wraps the value in POSIX single quotes, escaping any embedded
// single quotes by closing, inserting a backslash-quote, then reopening.
// The result is safe to paste into an `sh -c` snippet.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
