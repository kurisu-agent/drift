package exec

import (
	"errors"
	"reflect"
	"testing"
)

func TestLinkerWrap(t *testing.T) {
	t.Parallel()

	const prefix = "/data/data/com.termux/files/usr"
	const linker = "/system/bin/linker64"
	const sshPath = prefix + "/bin/ssh"

	lookPathSSH := func(name string) (string, error) {
		if name == "ssh" {
			return sshPath, nil
		}
		return "", errors.New("not found")
	}
	linkerExists := func(p string) bool { return p == linker }

	cases := []struct {
		name     string
		bin      string
		args     []string
		prefix   string
		linker   string
		lookPath func(string) (string, error)
		exists   func(string) bool
		wantBin  string
		wantArgs []string
	}{
		{
			name:     "non-termux passthrough",
			bin:      "ssh",
			args:     []string{"user@host"},
			prefix:   "",
			linker:   linker,
			lookPath: lookPathSSH,
			exists:   linkerExists,
			wantBin:  "ssh",
			wantArgs: []string{"user@host"},
		},
		{
			name:     "termux rewrites prefix binary through linker",
			bin:      "ssh",
			args:     []string{"user@host", "cmd"},
			prefix:   prefix,
			linker:   linker,
			lookPath: lookPathSSH,
			exists:   linkerExists,
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
			wantBin:  "/system/bin/sh",
			wantArgs: []string{"-c", "true"},
		},
		{
			name:     "missing linker falls back to passthrough",
			bin:      "ssh",
			args:     []string{"user@host"},
			prefix:   prefix,
			linker:   linker,
			lookPath: lookPathSSH,
			exists:   func(string) bool { return false },
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
			wantBin:  "nonexistent",
			wantArgs: []string{"arg"},
		},
		{
			name:     "trailing slash on prefix is tolerated",
			bin:      "ssh",
			args:     nil,
			prefix:   prefix + "/",
			linker:   linker,
			lookPath: lookPathSSH,
			exists:   linkerExists,
			wantBin:  linker,
			wantArgs: []string{sshPath},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			gotBin, gotArgs := linkerWrap(tc.bin, tc.args, tc.prefix, tc.linker, tc.lookPath, tc.exists)
			if gotBin != tc.wantBin {
				t.Errorf("bin = %q, want %q", gotBin, tc.wantBin)
			}
			if !reflect.DeepEqual(gotArgs, tc.wantArgs) {
				t.Errorf("args = %v, want %v", gotArgs, tc.wantArgs)
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
