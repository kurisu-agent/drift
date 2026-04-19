package main

import (
	"os"
	"reflect"
	"testing"
)

func TestCliArgs(t *testing.T) {
	exe, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable: %v", err)
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
