// Binary devpod-shim records every invocation as a JSON line in
// /tmp/devpod-invocations.log and copies the file/dir artifacts drift
// hands to devpod into /tmp/devpod-artifacts/<ns-ts>-<sub>/, then exits 0.
//
// It stands in for the real devpod during tune/feature/dotfiles composition
// tests so assertions can focus both on the argv drift produced *and* on
// the actual file content drift materialized (layer-1 dotfiles dir, starter
// tmp source dir, --extra-devcontainer-path file). The harness installs it
// at /usr/local/bin/devpod and reads back both the log and the artifact
// tree via docker exec.
package main

import (
	"encoding/json"
	"fmt"
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
}

func main() {
	argv := os.Args[1:]

	sub := ""
	if len(argv) >= 1 {
		sub = argv[0]
	}
	// `agent workspace install-dotfiles` — two words deeper than the usual
	// subcommand. Capture the tail verb so the artifact dir name hints at
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

	// Log entry — argv plus the dir the caller can read back.
	if err := appendLog(record{Argv: argv, ArtifactDir: artDir}); err != nil {
		fmt.Fprintf(os.Stderr, "devpod-shim: log: %v\n", err)
		os.Exit(3)
	}

	// Canned responses for query subcommands so drift's kart.info /
	// kart.list paths work without a real daemon. Up/stop/delete/
	// install-dotfiles etc. are acknowledged silently.
	switch argv[0] {
	case "status":
		fmt.Println(`{"state":"Running"}`)
	case "list":
		fmt.Println(`[]`)
	}
	os.Exit(0)
}

// copyKnownArtifacts inspects argv for paths drift materialized (starter
// tmpdir, layer-1 dotfiles tmpdir, --extra-devcontainer-path file) and
// preserves copies under dir so tests can assert on the contents after the
// shim exits — drift removes these tmpdirs on defer.
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
			// file:// URL pointing at a tmpdir drift materialized.
			if i+1 < len(argv) {
				if path, ok := strings.CutPrefix(argv[i+1], "file://"); ok {
					_ = copyTree(path, filepath.Join(dir, "dotfiles"))
				}
			}
		case "--id":
			// Devpod's `up --id <name> <source>` convention: source is two
			// positions after --id. Only copy when source is a local dir;
			// clone URLs skip (os.Stat fails cleanly).
			if i+2 < len(argv) {
				src := argv[i+2]
				if st, err := os.Stat(src); err == nil && st.IsDir() {
					_ = copyTree(src, filepath.Join(dir, "source"))
				}
			}
		}
	}
}

// appendLog writes one JSON-encoded record per line, O_APPEND so parallel
// invocations (if any) don't stomp each other.
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

// sanitize replaces anything awkward for a filesystem path with '-'. Keeps
// the shim tolerant of unexpected subcommand shapes.
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
