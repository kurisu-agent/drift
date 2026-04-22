package run_test

import (
	"testing"

	"github.com/kurisu-agent/drift/internal/run"
)

// TestMergeBuiltinDefaults_backfillsArgsOnUntouchedBuiltin pins the bug
// this function exists to patch: a circuit seeded by an older lakitu has
// a `ping` entry with no args: declaration. The embedded default does
// have one. When commands match verbatim, the merge copies args over so
// the client can prompt for a host.
func TestMergeBuiltinDefaults_backfillsArgsOnUntouchedBuiltin(t *testing.T) {
	cmd := "ping -c 4 {{ .Arg 0 | shq }}"
	user := &run.Registry{Entries: map[string]run.Entry{
		"ping": {
			Name:        "ping",
			Description: "Ping a host",
			Mode:        run.ModeOutput,
			Command:     cmd,
		},
	}}
	defaults := &run.Registry{Entries: map[string]run.Entry{
		"ping": {
			Name:    "ping",
			Mode:    run.ModeOutput,
			Command: cmd,
			Args: []run.ArgSpec{
				{Name: "host", Prompt: "Host to ping", Type: run.ArgTypeInput, Default: "1.1.1.1"},
			},
		},
	}}

	run.MergeBuiltinDefaults(user, defaults)

	got := user.Entries["ping"]
	if len(got.Args) != 1 || got.Args[0].Name != "host" || got.Args[0].Default != "1.1.1.1" {
		t.Errorf("merged args = %+v, want one host input with default", got.Args)
	}
	// Fields the user set must be preserved — we only back-fill args.
	if got.Description != "Ping a host" {
		t.Errorf("description clobbered: %q", got.Description)
	}
}

// TestMergeBuiltinDefaults_skipsEntriesWithCustomizedCommand is the
// safety gate: if the user modified their command, our embedded args
// shape may not match and we must not force declarations onto it.
func TestMergeBuiltinDefaults_skipsEntriesWithCustomizedCommand(t *testing.T) {
	user := &run.Registry{Entries: map[string]run.Entry{
		"ping": {
			Name:    "ping",
			Mode:    run.ModeOutput,
			Command: "fping -c 4 {{ .Arg 0 | shq }}", // user edited
		},
	}}
	defaults := &run.Registry{Entries: map[string]run.Entry{
		"ping": {
			Name:    "ping",
			Mode:    run.ModeOutput,
			Command: "ping -c 4 {{ .Arg 0 | shq }}",
			Args:    []run.ArgSpec{{Name: "host", Default: "1.1.1.1"}},
		},
	}}

	run.MergeBuiltinDefaults(user, defaults)

	if len(user.Entries["ping"].Args) != 0 {
		t.Errorf("args merged onto customized command: %+v", user.Entries["ping"].Args)
	}
}

// TestMergeBuiltinDefaults_preservesUserDeclaredArgs: when the user has
// already declared their own args (e.g. added a prompt manually), we
// must not overwrite them.
func TestMergeBuiltinDefaults_preservesUserDeclaredArgs(t *testing.T) {
	cmd := "ping -c 4 {{ .Arg 0 | shq }}"
	user := &run.Registry{Entries: map[string]run.Entry{
		"ping": {
			Name:    "ping",
			Mode:    run.ModeOutput,
			Command: cmd,
			Args:    []run.ArgSpec{{Name: "target", Default: "example.com"}},
		},
	}}
	defaults := &run.Registry{Entries: map[string]run.Entry{
		"ping": {
			Name:    "ping",
			Mode:    run.ModeOutput,
			Command: cmd,
			Args:    []run.ArgSpec{{Name: "host", Default: "1.1.1.1"}},
		},
	}}

	run.MergeBuiltinDefaults(user, defaults)

	got := user.Entries["ping"].Args
	if len(got) != 1 || got[0].Name != "target" || got[0].Default != "example.com" {
		t.Errorf("user args overwritten: %+v", got)
	}
}

// TestMergeBuiltinDefaults_ignoresNonBuiltinEntries: user-defined
// entries don't appear in the defaults registry, so the function must
// leave them alone.
func TestMergeBuiltinDefaults_ignoresNonBuiltinEntries(t *testing.T) {
	user := &run.Registry{Entries: map[string]run.Entry{
		"deploy": {
			Name:    "deploy",
			Mode:    run.ModeOutput,
			Command: "deploy.sh {{ .Arg 0 }}",
		},
	}}
	defaults := &run.Registry{Entries: map[string]run.Entry{
		"ping": {
			Name: "ping", Mode: run.ModeOutput,
			Command: "ping", Args: []run.ArgSpec{{Name: "host"}},
		},
	}}

	run.MergeBuiltinDefaults(user, defaults)

	if _, exists := user.Entries["ping"]; exists {
		t.Errorf("defaults leaked a new entry into user registry")
	}
	if len(user.Entries["deploy"].Args) != 0 {
		t.Errorf("deploy gained args: %+v", user.Entries["deploy"].Args)
	}
}

// TestMergeBuiltinDefaults_nilSafe: defensive check so a missing defaults
// registry (e.g. the embedded yaml failed to parse) just silently skips
// the merge rather than panicking in the RPC hot path.
func TestMergeBuiltinDefaults_nilSafe(t *testing.T) {
	run.MergeBuiltinDefaults(nil, nil)
	reg := &run.Registry{Entries: map[string]run.Entry{}}
	run.MergeBuiltinDefaults(reg, nil)
	run.MergeBuiltinDefaults(nil, reg)
}
