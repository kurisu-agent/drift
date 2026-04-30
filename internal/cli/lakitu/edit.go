package lakitu

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	osexec "os/exec"

	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/wire"
	"gopkg.in/yaml.v3"
)

// runTuneEdit fetches the current tune YAML, opens $EDITOR (or vi)
// on a tempfile, and submits tune.replace on save. Validation errors
// prompt the user to reopen the editor on the same tempfile.
func runTuneEdit(ctx context.Context, io IO, cmd tuneEditCmd) int {
	return runEdit(ctx, io, editSpec{
		kind:          "tune",
		name:          cmd.Name,
		showMethod:    wire.MethodTuneShow,
		replaceMethod: wire.MethodTuneReplace,
		marshalShow: func(raw json.RawMessage) ([]byte, error) {
			var r server.TuneResult
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, fmt.Errorf("decode show: %w", err)
			}
			return yaml.Marshal(r.Tune)
		},
		buildReplaceParams: func(name, yaml string) any {
			return server.TuneReplaceParams{Name: name, YAML: yaml}
		},
	})
}

func runCharacterEdit(ctx context.Context, io IO, cmd characterEditCmd) int {
	return runEdit(ctx, io, editSpec{
		kind:          "character",
		name:          cmd.Name,
		showMethod:    wire.MethodCharacterShow,
		replaceMethod: wire.MethodCharacterReplace,
		marshalShow: func(raw json.RawMessage) ([]byte, error) {
			var r server.CharacterResult
			if err := json.Unmarshal(raw, &r); err != nil {
				return nil, fmt.Errorf("decode show: %w", err)
			}
			return yaml.Marshal(r.Character)
		},
		buildReplaceParams: func(name, yaml string) any {
			return server.CharacterReplaceParams{Name: name, YAML: yaml}
		},
	})
}

// editSpec pins the per-object details the generic editor flow
// needs. Keeping the flow itself in one place so tune and character
// behave identically around the $EDITOR loop.
type editSpec struct {
	kind               string // "tune" or "character", used in tempfile names and user-facing text
	name               string
	showMethod         string
	replaceMethod      string
	marshalShow        func(json.RawMessage) ([]byte, error)
	buildReplaceParams func(name, yaml string) any
}

func runEdit(ctx context.Context, iob IO, spec editSpec) int {
	if !isTTY(iob.Stdin) {
		return errfmt.Emit(iob.Stderr, fmt.Errorf("%s edit requires a TTY (stdin is not a terminal)", spec.kind))
	}
	if spec.name == "" {
		return errfmt.Emit(iob.Stderr, fmt.Errorf("%s edit: name is required", spec.kind))
	}

	current, err := fetchForEdit(ctx, spec)
	if err != nil {
		return errfmt.Emit(iob.Stderr, err)
	}

	tmp, err := writeEditTempfile(spec.kind, spec.name, current)
	if err != nil {
		return errfmt.Emit(iob.Stderr, err)
	}
	defer func() { _ = os.Remove(tmp) }()

	for {
		if err := openInEditor(ctx, tmp); err != nil {
			return errfmt.Emit(iob.Stderr, err)
		}
		edited, err := os.ReadFile(tmp)
		if err != nil {
			return errfmt.Emit(iob.Stderr, fmt.Errorf("re-read tempfile: %w", err))
		}
		if bytes.Equal(bytes.TrimSpace(edited), bytes.TrimSpace(current)) {
			fmt.Fprintf(iob.Stderr, "%s %q: no changes\n", spec.kind, spec.name)
			return 0
		}
		result, rpcErr := submitReplace(ctx, spec, string(edited))
		if rpcErr == nil {
			// Pretty-print the result YAML so users see the server's
			// normalized shape.
			fmt.Fprintln(iob.Stdout, string(result))
			return 0
		}

		// Surface the error and offer a reopen. On non-TTY or decline
		// we keep the tempfile around (logged) so the user doesn't
		// lose work.
		fmt.Fprintf(iob.Stderr, "error: %v\n", rpcErr)
		fmt.Fprintf(iob.Stderr, "  tempfile preserved at %s\n", tmp)
		if !askReopen(iob) {
			// Leak the tempfile: the user wants to recover the
			// content from disk, so don't let defer clean it up.
			tmp = ""
			return 1
		}
	}
}

func fetchForEdit(ctx context.Context, spec editSpec) ([]byte, error) {
	raw, err := json.Marshal(map[string]string{"name": spec.name})
	if err != nil {
		return nil, fmt.Errorf("marshal show params: %w", err)
	}
	req := &wire.Request{
		JSONRPC: wire.Version,
		Method:  spec.showMethod,
		Params:  raw,
		ID:      json.RawMessage(`1`),
	}
	reg, regErr := buildRegistry(ctx, false, false)
	if regErr != nil {
		return nil, regErr
	}
	resp := reg.Dispatch(ctx, req)
	if resp.Error != nil {
		return nil, rpcerr.FromWire(resp.Error)
	}
	yamlBytes, err := spec.marshalShow(resp.Result)
	if err != nil {
		return nil, err
	}
	return yamlBytes, nil
}

func submitReplace(ctx context.Context, spec editSpec, yamlDoc string) ([]byte, error) {
	raw, err := json.Marshal(spec.buildReplaceParams(spec.name, yamlDoc))
	if err != nil {
		return nil, fmt.Errorf("marshal replace params: %w", err)
	}
	req := &wire.Request{
		JSONRPC: wire.Version,
		Method:  spec.replaceMethod,
		Params:  raw,
		ID:      json.RawMessage(`1`),
	}
	reg, regErr := buildRegistry(ctx, false, false)
	if regErr != nil {
		return nil, regErr
	}
	resp := reg.Dispatch(ctx, req)
	if resp.Error != nil {
		return nil, rpcerr.FromWire(resp.Error)
	}
	var v any
	if err := json.Unmarshal(resp.Result, &v); err != nil {
		return nil, fmt.Errorf("decode replace result: %w", err)
	}
	return json.MarshalIndent(v, "", "  ")
}

func writeEditTempfile(kind, name string, contents []byte) (string, error) {
	f, err := os.CreateTemp("", fmt.Sprintf("lakitu-%s-%s-*.yaml", kind, name))
	if err != nil {
		return "", fmt.Errorf("mktemp: %w", err)
	}
	defer func() { _ = f.Close() }()
	if _, err := f.Write(contents); err != nil {
		_ = os.Remove(f.Name())
		return "", fmt.Errorf("write tempfile: %w", err)
	}
	return f.Name(), nil
}

func openInEditor(ctx context.Context, path string) error {
	bin := os.Getenv("EDITOR")
	if bin == "" {
		bin = "vi"
	}
	// User-supplied $EDITOR + a tempfile path we just created ourselves.
	// Launching the user's own editor on a controlled path is the whole
	// point of this function.
	//nolint:gosec // G204: intentional, see above
	cmd := osexec.CommandContext(ctx, bin, path)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		var ee *osexec.ExitError
		if errors.As(err, &ee) && ee.ExitCode() != 0 {
			return fmt.Errorf("editor %q exited %d", bin, ee.ExitCode())
		}
		return fmt.Errorf("editor %q failed: %w", bin, err)
	}
	return nil
}

// askReopen prompts once. Defaults to Yes to match git-commit-on-hook-
// failure ergonomics — a user who ^C's out of $EDITOR already has a
// clean exit path.
func askReopen(iob IO) bool {
	fmt.Fprint(iob.Stderr, "reopen editor? [Y/n] ")
	buf := make([]byte, 8)
	n, _ := iob.Stdin.Read(buf)
	if n == 0 {
		return true
	}
	switch buf[0] {
	case 'n', 'N':
		return false
	default:
		return true
	}
}

func isTTY(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok {
		return false
	}
	stat, err := f.Stat()
	if err != nil {
		return false
	}
	return (stat.Mode() & os.ModeCharDevice) != 0
}
