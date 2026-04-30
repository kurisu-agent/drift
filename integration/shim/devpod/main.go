// Binary devpod-shim stands in for devpod during integration tests:
// records every invocation as a JSON line in /tmp/devpod-invocations.log,
// copies the file/dir artifacts drift hands to devpod into
// /tmp/devpod-artifacts/<ns-ts>-<sub>/, captures stdin (so tests can
// assert on the post-up `ssh --command 'bash -s'` script body), emits
// canned JSON for query subcommands, and exits 0. The harness installs
// it at /usr/local/bin/devpod and reads both back via docker exec.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	logPath      = "/tmp/devpod-invocations.log"
	artifactsDir = "/tmp/devpod-artifacts"
)

type record struct {
	Argv        []string `json:"argv"`
	ArtifactDir string   `json:"artifact_dir,omitempty"`
	// Env captures the process env at invocation time — used by env-
	// injection tests to confirm that chest-backed build-time secrets
	// reach the install-dotfiles call without landing in containerEnv.
	Env []string `json:"env,omitempty"`
}

func main() {
	argv := os.Args[1:]

	sub := ""
	if len(argv) >= 1 {
		sub = argv[0]
	}
	// `agent workspace install-dotfiles` is two words deeper than a normal
	// subcommand — capture the tail verb so the artifact dir name hints at
	// which invocation a test is inspecting.
	if sub == "agent" && len(argv) >= 3 {
		sub = argv[2]
	}

	artDir := filepath.Join(artifactsDir,
		strconv.FormatInt(time.Now().UnixNano(), 10)+"-"+sanitize(sub))
	if err := os.MkdirAll(artDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "devpod-shim: mkdir %s: %v\n", artDir, err)
		os.Exit(3)
	}

	copyKnownArtifacts(argv, artDir)

	// Drain stdin and capture it. The post-up `ssh --command 'bash -s'`
	// path pipes the assembled script (including the gh-auth fragment's
	// PAT) over stdin; tests assert on the captured bytes. We always
	// drain so the parent's pipe write never blocks, then only emit a
	// stdin file when something was actually piped in — the absence of
	// the file is itself useful evidence that an invocation didn't
	// receive script input.
	if stdin, err := io.ReadAll(os.Stdin); err == nil && len(stdin) > 0 {
		if werr := os.WriteFile(filepath.Join(artDir, "stdin"), stdin, 0o600); werr != nil {
			fmt.Fprintf(os.Stderr, "devpod-shim: write stdin: %v\n", werr)
			os.Exit(3)
		}
	}

	if err := appendLog(record{Argv: argv, ArtifactDir: artDir, Env: os.Environ()}); err != nil {
		fmt.Fprintf(os.Stderr, "devpod-shim: log: %v\n", err)
		os.Exit(3)
	}

	// Canned responses for query subcommands so drift's kart.info /
	// kart.list paths work without a real daemon.
	switch argv[0] {
	case "status":
		fmt.Println(`{"state":"Running"}`)
	case "list":
		fmt.Println(`[]`)
	}
	os.Exit(0)
}

// copyKnownArtifacts preserves paths drift materialized (starter tmpdir,
// layer-1 dotfiles, --extra-devcontainer-path file) under dir — drift
// RemoveAlls these on defer, so tests can only assert after the shim runs.
func copyKnownArtifacts(argv []string, dir string) {
	for i, a := range argv {
		switch a {
		case "--extra-devcontainer-path":
			if i+1 < len(argv) {
				_ = copyFile(argv[i+1], filepath.Join(dir, "extra-devcontainer.json"))
			}
		case "--dotfiles", "--repository":
			// `up --dotfiles` (layer-2) and `agent workspace install-dotfiles
			// --repository` (layer-1, skevetter fork v0.22) both carry a
			// file:// URL pointing at a drift-materialized tmpdir.
			if i+1 < len(argv) {
				if path, ok := strings.CutPrefix(argv[i+1], "file://"); ok {
					_ = copyTree(path, filepath.Join(dir, "dotfiles"))
				}
			}
		case "--id":
			// `up --id <name> <source>`: source is two positions after --id.
			// Only copy when source is a local dir; clone URLs skip (stat
			// fails cleanly).
			if i+2 < len(argv) {
				src := argv[i+2]
				if st, err := os.Stat(src); err == nil && st.IsDir() {
					_ = copyTree(src, filepath.Join(dir, "source"))
				}
			}
		}
	}
}

// appendLog uses O_APPEND so parallel invocations don't stomp each other.
func appendLog(r record) error {
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o666)
	if err != nil {
		return err
	}
	defer func() { _ = f.Close() }()
	buf, err := json.Marshal(r)
	if err != nil {
		return err
	}
	buf = append(buf, '\n')
	_, err = f.Write(buf)
	return err
}

func sanitize(s string) string {
	if s == "" {
		return "unknown"
	}
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z', c >= '0' && c <= '9', c == '-', c == '_':
			out = append(out, c)
		default:
			out = append(out, '-')
		}
	}
	return string(out)
}

func copyFile(src, dst string) error {
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, b, 0o644)
}

func copyTree(src, dst string) error {
	return filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		out := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(out, b, info.Mode().Perm())
	})
}
