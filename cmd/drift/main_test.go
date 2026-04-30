package main

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestCliArgs(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
	}

	// A differently-spelled path to the same inode exercises the
	// os.SameFile fallback — Termux sometimes gives us argv[1] under a
	// different canonicalization than /proc/self/exe reports.
	alias := filepath.Join(t.TempDir(), "drift-alias")
	if err := os.Symlink(exe, alias); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	cases := []struct {
		name string
		argv []string
		want []string
	}{
		{"empty", []string{exe}, nil},
		{"normal", []string{exe, "status"}, []string{"status"}},
		{"termux_injection", []string{exe, exe, "--version"}, []string{"--version"}},
		{"termux_injection_no_other_args", []string{exe, exe}, []string{}},
		{"termux_injection_via_symlink", []string{exe, alias, "--version"}, []string{"--version"}},
		{"path_that_isnt_self", []string{exe, "/some/other/path"}, []string{"/some/other/path"}},
		{"flag_looks_like_path", []string{exe, "--output=json"}, []string{"--output=json"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := cliArgs(tc.argv)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("cliArgs(%v) = %v, want %v", tc.argv, got, tc.want)
			}
		})
	}
}

// TestCliArgs_TermuxLinkerWrap simulates the real-world Termux case that
// bit v0.2.1/v0.2.2: /proc/self/exe points at the Android linker, not
// our binary, so os.Executable (and by extension os.SameFile) can't
// identify argv[1] as self. termux-exec exports TERMUX_EXEC__PROC_SELF_EXE
// with the true binary path — using it must take precedence.
func TestCliArgs_TermuxLinkerWrap(t *testing.T) {
	// A path that doesn't exist and isn't our executable — the only way
	// to strip it is by consulting the env var.
	fakeBinary := "/data/data/com.termux/files/usr/bin/drift"
	t.Setenv("TERMUX_EXEC__PROC_SELF_EXE", fakeBinary)

	got := cliArgs([]string{"drift", fakeBinary, "help"})
	want := []string{"help"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("cliArgs under Termux linker wrap = %v, want %v", got, want)
	}
}
