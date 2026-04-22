package run_test

import (
	"strings"
	"testing"

	"github.com/kurisu-agent/drift/internal/run"
)

func TestParse_minimal(t *testing.T) {
	buf := []byte(`
runs:
  ai:
    description: "Claude"
    mode: interactive
    command: 'exec claude'
  uptime:
    mode: output
    command: 'uptime'
`)
	reg, err := run.Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ai, ok := reg.Get("ai")
	if !ok {
		t.Fatalf("missing ai entry")
	}
	if ai.Mode != run.ModeInteractive {
		t.Errorf("ai.Mode = %q, want interactive", ai.Mode)
	}
	if ai.Command != "exec claude" {
		t.Errorf("ai.Command = %q", ai.Command)
	}
	if ai.Name != "ai" {
		t.Errorf("ai.Name = %q, want ai", ai.Name)
	}
	sorted := reg.Sorted()
	if len(sorted) != 2 || sorted[0].Name != "ai" || sorted[1].Name != "uptime" {
		t.Errorf("sorted order wrong: %+v", sorted)
	}
}

func TestParse_rejectsMissingMode(t *testing.T) {
	buf := []byte(`runs:
  bad:
    command: 'echo hi'`)
	if _, err := run.Parse(buf); err == nil || !strings.Contains(err.Error(), "mode required") {
		t.Fatalf("want mode-required error, got %v", err)
	}
}

func TestParse_rejectsUnknownPost(t *testing.T) {
	buf := []byte(`runs:
  bad:
    mode: interactive
    post: totally-bogus
    command: 'true'`)
	if _, err := run.Parse(buf); err == nil || !strings.Contains(err.Error(), "unknown post hook") {
		t.Fatalf("want unknown-post error, got %v", err)
	}
}

func TestParse_rejectsBadName(t *testing.T) {
	buf := []byte(`runs:
  "BadName":
    mode: output
    command: 'true'`)
	if _, err := run.Parse(buf); err == nil || !strings.Contains(err.Error(), "invalid entry name") {
		t.Fatalf("want invalid-name error, got %v", err)
	}
}

func TestLoad_missingFileIsEmpty(t *testing.T) {
	reg, err := run.Load(t.TempDir() + "/nope.yaml")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(reg.Entries) != 0 {
		t.Errorf("want empty registry, got %d entries", len(reg.Entries))
	}
}

// TestParse_argsRoundtrip: happy-path coverage for each declared arg type
// so the client-side prompt machinery is guaranteed the shape it needs.
func TestParse_argsRoundtrip(t *testing.T) {
	buf := []byte(`
runs:
  deploy:
    mode: output
    args:
      - name: host
        prompt: "Host"
        type: input
        default: "1.1.1.1"
      - name: body
        type: text
      - name: env
        type: select
        options: [dev, staging, prod]
        default: staging
    command: 'echo {{ .Arg 0 }} {{ .Arg 1 }} {{ .Arg 2 }}'
`)
	reg, err := run.Parse(buf)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	e, _ := reg.Get("deploy")
	if len(e.Args) != 3 {
		t.Fatalf("args = %d, want 3", len(e.Args))
	}
	if e.Args[0].Default != "1.1.1.1" || e.Args[0].Type != run.ArgTypeInput {
		t.Errorf("host spec = %+v", e.Args[0])
	}
	if e.Args[1].Type != run.ArgTypeText {
		t.Errorf("body.type = %q", e.Args[1].Type)
	}
	if e.Args[2].Type != run.ArgTypeSelect || len(e.Args[2].Options) != 3 || e.Args[2].Default != "staging" {
		t.Errorf("env spec = %+v", e.Args[2])
	}
}

// TestParse_argsValidation locks down the failure modes the schema guards
// against — one case per rule so authors get a specific error when they
// trip something.
func TestParse_argsValidation(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string
	}{
		{
			name: "duplicate arg name",
			body: `runs:
  bad:
    mode: output
    args:
      - name: host
      - name: host
    command: 'true'`,
			want: "duplicate name",
		},
		{
			name: "unknown type",
			body: `runs:
  bad:
    mode: output
    args:
      - name: host
        type: radio
    command: 'true'`,
			want: "unknown type",
		},
		{
			name: "select without options",
			body: `runs:
  bad:
    mode: output
    args:
      - name: env
        type: select
    command: 'true'`,
			want: "select requires options",
		},
		{
			name: "select default not in options",
			body: `runs:
  bad:
    mode: output
    args:
      - name: env
        type: select
        options: [a, b]
        default: c
    command: 'true'`,
			want: "not in options",
		},
		{
			name: "options on non-select",
			body: `runs:
  bad:
    mode: output
    args:
      - name: host
        options: [a, b]
    command: 'true'`,
			want: "options only valid for type",
		},
		{
			name: "missing arg name",
			body: `runs:
  bad:
    mode: output
    args:
      - prompt: "huh"
    command: 'true'`,
			want: "name required",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := run.Parse([]byte(tc.body))
			if err == nil {
				t.Fatalf("want error containing %q, got nil", tc.want)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want %q substring", err, tc.want)
			}
		})
	}
}
