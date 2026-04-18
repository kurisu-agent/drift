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
		"GLOBAL FLAGS",
		"--debug",
		"COMMANDS",
		"foo — Foo-related commands.",
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
	// Kong's auto-generated --help flag is noise to an LLM.
	if strings.Contains(got, "--help") {
		t.Errorf("--help flag should be filtered, got:\n%s", got)
	}
	// Empty-body section is dropped.
	if strings.Contains(got, "SKIP ME") {
		t.Errorf("empty-body section leaked into output:\n%s", got)
	}
}
