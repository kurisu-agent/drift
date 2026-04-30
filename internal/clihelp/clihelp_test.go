package clihelp_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/alecthomas/kong"
	"github.com/kurisu-agent/drift/internal/clihelp"
)

type fakeCLI struct {
	Debug bool `help:"Verbose output."`

	Foo struct {
		Bar struct {
			Name string `arg:""`
		} `cmd:"" help:"Do the Bar thing."`
	} `cmd:"" help:"Foo-related commands."`

	Hidden struct{} `cmd:"" hidden:"" help:"internal"`
}

func TestRender_CommandsAndFlagsAndSections(t *testing.T) {
	var cli fakeCLI
	parser, err := kong.New(&cli,
		kong.Name("tool"),
		kong.Description("A test CLI."),
		kong.Exit(func(int) {}),
	)
	if err != nil {
		t.Fatalf("kong.New: %v", err)
	}

	var buf bytes.Buffer
	if err := clihelp.Render(&buf, clihelp.Doc{
		App:   parser,
		Intro: "Point of context.",
		Sections: []clihelp.Section{
			{Title: "RPC METHODS", Body: "  server.version\n  kart.list\n"},
			{Title: "SKIP ME", Body: "   "}, // whitespace-only → skipped
		},
	}); err != nil {
		t.Fatalf("Render: %v", err)
	}
	got := buf.String()

	for _, want := range []string{
		"NAME\n  tool — A test CLI.",
		"Point of context.",
		// COMMANDS header directs callers to per-command --help for flags;
		// the catalog itself is leaf-only to stay terse.
		"COMMANDS (run `tool <cmd> --help` for flags)",
		"foo bar — Do the Bar thing.",
		"RPC METHODS",
		"server.version",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q\n---\n%s", want, got)
		}
	}
	// Hidden commands must not leak.
	if strings.Contains(got, "hidden") {
		t.Errorf("hidden command leaked into output:\n%s", got)
	}
	// Branch nodes without their own semantics are elided — we render
	// leaves only ("foo bar"), not the "foo" parent entry.
	if strings.Contains(got, "foo — Foo-related commands.") {
		t.Errorf("branch node leaked into leaf-only catalog:\n%s", got)
	}
	// Empty-body section is dropped.
	if strings.Contains(got, "SKIP ME") {
		t.Errorf("empty-body section leaked into output:\n%s", got)
	}
}
