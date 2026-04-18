package drift

import (
	"strings"
	"testing"
)

func TestBuildAIArgv_SSH(t *testing.T) {
	bin, argv := buildAIArgv(false, "box", false)
	if bin != "ssh" {
		t.Fatalf("bin = %q, want ssh", bin)
	}
	// Expect: -t drift.box '<remoteAICmd>'
	if argv[0] != "-t" {
		t.Errorf("argv[0] = %q, want -t", argv[0])
	}
	if argv[len(argv)-2] != "drift.box" {
		t.Errorf("target arg = %q, want drift.box", argv[len(argv)-2])
	}
	if !strings.Contains(argv[len(argv)-1], "claude --dangerously-skip-permissions") {
		t.Errorf("remote cmd missing claude flag: %q", argv[len(argv)-1])
	}
	if !strings.Contains(argv[len(argv)-1], `"$HOME/.drift"`) {
		t.Errorf("remote cmd missing cd target: %q", argv[len(argv)-1])
	}
}

func TestBuildAIArgv_SSHForwardAgent(t *testing.T) {
	_, argv := buildAIArgv(false, "box", true)
	// -A should appear after -t.
	if argv[0] != "-t" || argv[1] != "-A" {
		t.Errorf("argv[:2] = %v, want [-t -A]", argv[:2])
	}
}

func TestBuildAIArgv_Mosh(t *testing.T) {
	bin, argv := buildAIArgv(true, "box", false)
	if bin != "mosh" {
		t.Fatalf("bin = %q, want mosh", bin)
	}
	if argv[0] != "drift.box" {
		t.Errorf("target arg = %q, want drift.box", argv[0])
	}
	if argv[1] != "--" {
		t.Errorf("argv[1] = %q, want --", argv[1])
	}
	// After `--`, mosh hands argv to the remote shell. We wrap in `sh -c`
	// so the heredoc-style remote command runs under a shell.
	if argv[2] != "sh" || argv[3] != "-c" {
		t.Errorf("argv[2:4] = %v, want [sh -c]", argv[2:4])
	}
}
