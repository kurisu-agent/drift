package exec

import (
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
)

func TestLinkerWrap(t *testing.T) {
	t.Parallel()

	const prefix = "/data/data/com.termux/files/usr"
	const linker = "/system/bin/linker64"
	const sshPath = prefix + "/bin/ssh"
	const moshPath = prefix + "/bin/mosh"
	const perlPath = prefix + "/bin/perl"

	lookPath := func(name string) (string, error) {
		switch name {
		case "ssh":
			return sshPath, nil
		case "mosh":
			return moshPath, nil
		case "mosh-envwrap":
			return prefix + "/bin/mosh-envwrap", nil
		case "mosh-syssh":
			return prefix + "/bin/mosh-syssh", nil
		}
		return "", errors.New("not found")
	}
	linkerExists := func(p string) bool { return p == linker }

	// Non-shebang case: report ok=false (ELF or unknown).
	noShebang := func(string) (string, string, bool) { return "", "", false }
	// mosh-like: shebang points to perl under $PREFIX.
	moshShebang := func(path string) (string, string, bool) {
		if path == moshPath {
			return perlPath, "", true
		}
		return "", "", false
	}
	// Shebang with an interpreter argument (e.g. `#!/bin/sh -e`).
	shebangWithArg := func(path string) (string, string, bool) {
		if path == prefix+"/bin/mosh-envwrap" {
			return perlPath, "-w", true
		}
		return "", "", false
	}
	// Shebang pointing outside $PREFIX — kernel can handle this natively.
	shebangOutsidePrefix := func(path string) (string, string, bool) {
		if path == prefix+"/bin/mosh-syssh" {
			return "/system/bin/sh", "", true
		}
		return "", "", false
	}

	cases := []struct {
		name     string
		bin      string
		args     []string
		prefix   string
		linker   string
		lookPath func(string) (string, error)
		exists   func(string) bool
		shebang  func(string) (string, string, bool)
		wantBin  string
		wantArgs []string
	}{
		{
			name:     "non-termux passthrough",
			bin:      "ssh",
			args:     []string{"user@host"},
			prefix:   "",
			linker:   linker,
			lookPath: lookPath,
			exists:   linkerExists,
			shebang:  noShebang,
			wantBin:  "ssh",
			wantArgs: []string{"user@host"},
		},
		{
			name:     "termux rewrites prefix binary through linker",
			bin:      "ssh",
			args:     []string{"user@host", "cmd"},
			prefix:   prefix,
			linker:   linker,
			lookPath: lookPath,
			exists:   linkerExists,
			shebang:  noShebang,
			wantBin:  linker,
			wantArgs: []string{sshPath, "user@host", "cmd"},
		},
		{
			name:   "termux leaves binaries outside PREFIX alone",
			bin:    "/system/bin/sh",
			args:   []string{"-c", "true"},
			prefix: prefix,
			linker: linker,
			lookPath: func(name string) (string, error) {
				return name, nil
			},
			exists:   linkerExists,
			shebang:  noShebang,
			wantBin:  "/system/bin/sh",
			wantArgs: []string{"-c", "true"},
		},
		{
			name:     "missing linker falls back to passthrough",
			bin:      "ssh",
			args:     []string{"user@host"},
			prefix:   prefix,
			linker:   linker,
			lookPath: lookPath,
			exists:   func(string) bool { return false },
			shebang:  noShebang,
			wantBin:  "ssh",
			wantArgs: []string{"user@host"},
		},
		{
			name:   "lookpath failure falls back to passthrough",
			bin:    "nonexistent",
			args:   []string{"arg"},
			prefix: prefix,
			linker: linker,
			lookPath: func(string) (string, error) {
				return "", errors.New("not found")
			},
			exists:   linkerExists,
			shebang:  noShebang,
			wantBin:  "nonexistent",
			wantArgs: []string{"arg"},
		},
		{
			name:     "trailing slash on prefix is tolerated",
			bin:      "ssh",
			args:     nil,
			prefix:   prefix + "/",
			linker:   linker,
			lookPath: lookPath,
			exists:   linkerExists,
			shebang:  noShebang,
			wantBin:  linker,
			wantArgs: []string{sshPath},
		},
		{
			name:     "script under PREFIX wraps interpreter, passes script as arg",
			bin:      "mosh",
			args:     []string{"user@host", "--"},
			prefix:   prefix,
			linker:   linker,
			lookPath: lookPath,
			exists:   linkerExists,
			shebang:  moshShebang,
			wantBin:  linker,
			wantArgs: []string{perlPath, moshPath, "user@host", "--"},
		},
		{
			name:     "shebang arg is preserved between interp and script",
			bin:      "mosh-envwrap",
			args:     []string{"arg1"},
			prefix:   prefix,
			linker:   linker,
			lookPath: lookPath,
			exists:   linkerExists,
			shebang:  shebangWithArg,
			wantBin:  linker,
			wantArgs: []string{perlPath, "-w", prefix + "/bin/mosh-envwrap", "arg1"},
		},
		{
			name:     "script whose interpreter lives outside PREFIX passes through",
			bin:      "mosh-syssh",
			args:     []string{"arg"},
			prefix:   prefix,
			linker:   linker,
			lookPath: lookPath,
			exists:   linkerExists,
			shebang:  shebangOutsidePrefix,
			wantBin:  "mosh-syssh",
			wantArgs: []string{"arg"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotBin, gotArgs := linkerWrap(tc.bin, tc.args, tc.prefix, tc.linker, tc.lookPath, tc.exists, tc.shebang)
			if gotBin != tc.wantBin {
				t.Errorf("bin = %q, want %q", gotBin, tc.wantBin)
			}
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Errorf("args = %v, want %v", gotArgs, tc.wantArgs)
			}
		})
	}
}

func TestReadShebang(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	cases := []struct {
		name       string
		content    string
		wantInterp string
		wantArg    string
		wantOK     bool
	}{
		{"plain shebang", "#!/usr/bin/perl\nprint 1;\n", "/usr/bin/perl", "", true},
		{"shebang with arg", "#!/usr/bin/perl -w\n", "/usr/bin/perl", "-w", true},
		{"shebang with multiple args kept as one", "#!/usr/bin/env -S perl -w\n", "/usr/bin/env", "-S perl -w", true},
		{"leading whitespace after bang", "#!   /bin/sh\n", "/bin/sh", "", true},
		{"no shebang", "print 1\n", "", "", false},
		{"ELF magic", "\x7fELF\x02\x01\x01\x00", "", "", false},
		{"empty file", "", "", "", false},
		{"just bang no interp", "#!\n", "", "", true},
	}
	for i, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			path := fmt.Sprintf("%s/%d", dir, i)
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			gotInterp, gotArg, gotOK := readShebang(path)
			if gotInterp != tc.wantInterp || gotArg != tc.wantArg || gotOK != tc.wantOK {
				t.Errorf("readShebang(%q) = (%q, %q, %v), want (%q, %q, %v)",
					tc.content, gotInterp, gotArg, gotOK, tc.wantInterp, tc.wantArg, tc.wantOK)
			}
		})
	}
}

func TestTermuxPrefix(t *testing.T) {
	cases := []struct {
		name         string
		termuxVer    string
		prefix       string
		wantDetected bool
	}{
		{"both unset", "", "", false},
		{"TERMUX_VERSION set", "0.118", "/data/data/com.termux/files/usr", true},
		{"PREFIX under com.termux", "", "/data/data/com.termux/files/usr", true},
		{"unrelated PREFIX ignored", "", "/usr", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("TERMUX_VERSION", tc.termuxVer)
			t.Setenv("PREFIX", tc.prefix)
			got := termuxPrefix()
			if tc.wantDetected && got == "" {
				t.Errorf("termuxPrefix() = %q, want non-empty", got)
			}
			if !tc.wantDetected && got != "" {
				t.Errorf("termuxPrefix() = %q, want empty", got)
			}
		})
	}
}
