package server

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
	"gopkg.in/yaml.v3"
)

// typeSkillNotFound mirrors the other handler-local error types — used
// when skill.resolve is called with a name that doesn't correspond to
// a directory under the circuit's skills root.
const typeSkillNotFound = rpcerr.Type("skill_not_found")

// SkillListHandler walks the circuit's Claude skills directory and
// returns one entry per `<skill>/SKILL.md` whose frontmatter parsed
// cleanly. A missing skills directory is not an error — a circuit that
// has never run claude simply has no skills, and the client renders
// that case as "no skills configured".
func (d *Deps) SkillListHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	root, err := d.skillsDir()
	if err != nil {
		return nil, rpcerr.Internal("skill.list: resolve skills dir: %v", err).Wrap(err)
	}
	skills, err := scanSkills(root)
	if err != nil {
		return nil, rpcerr.Internal("skill.list: %v", err).Wrap(err)
	}
	return wire.SkillListResult{Skills: skills}, nil
}

// SkillResolveHandler renders the interactive claude command the client
// will ssh/mosh to. The skill name is validated against the on-disk
// layout so a typo surfaces as skill_not_found rather than a claude
// session that silently ignores the prefix.
func (d *Deps) SkillResolveHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p wire.SkillResolveParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "skill.resolve: name is required")
	}
	root, err := d.skillsDir()
	if err != nil {
		return nil, rpcerr.Internal("skill.resolve: resolve skills dir: %v", err).Wrap(err)
	}
	if _, ok, err := readSkill(root, p.Name); err != nil {
		return nil, rpcerr.Internal("skill.resolve %q: %v", p.Name, err).Wrap(err)
	} else if !ok {
		return nil, rpcerr.NotFound(typeSkillNotFound, "skill %q not found", p.Name).With("name", p.Name)
	}
	return wire.SkillResolveResult{
		Name:    p.Name,
		Post:    wire.RunPostConnectLastScaffold,
		Command: renderSkillCommand(p.Name, p.Prompt),
	}, nil
}

// skillsDir resolves the circuit-side Claude skills root. Deps.SkillsDir
// overrides for tests; otherwise $HOME/.claude/skills (the user-level
// location Claude Code reads).
func (d *Deps) skillsDir() (string, error) {
	if d.SkillsDir != "" {
		return d.SkillsDir, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "skills"), nil
}

// scanSkills enumerates skill directories under root. Directories
// without a readable SKILL.md, or whose frontmatter fails to parse,
// are skipped — a malformed skill should not hide the others.
func scanSkills(root string) ([]wire.Skill, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", root, err)
	}
	out := make([]wire.Skill, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skill, ok, err := readSkill(root, e.Name())
		if err != nil || !ok {
			// Swallow individual-skill errors: listing is a discovery
			// surface, not a validation one. A broken SKILL.md would
			// otherwise break the entire picker.
			continue
		}
		out = append(out, skill)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// readSkill loads one skill's frontmatter. Returns ok=false when the
// directory exists but lacks SKILL.md, so the caller can decide
// whether that's a hard miss (resolve) or a quiet skip (list).
func readSkill(root, name string) (wire.Skill, bool, error) {
	path := filepath.Join(root, name, "SKILL.md")
	buf, err := os.ReadFile(path) // #nosec G304 -- path is rooted in a trusted skills dir under $HOME
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return wire.Skill{}, false, nil
		}
		return wire.Skill{}, false, err
	}
	fm, err := parseFrontmatter(buf)
	if err != nil {
		return wire.Skill{}, false, err
	}
	// Frontmatter `name` wins when present; fall back to the directory
	// name so a skill author who forgets the field still shows up.
	displayName := fm.Name
	if displayName == "" {
		displayName = name
	}
	return wire.Skill{Name: displayName, Description: fm.Description}, true, nil
}

type skillFrontmatter struct {
	Name        string `yaml:"name"`
	Description string `yaml:"description"`
}

// parseFrontmatter reads the leading `---\n…\n---\n` block from a
// Markdown file and YAML-decodes it. Files without a frontmatter block
// return a zero-value struct — the caller falls back to the directory
// name for Name and an empty description.
func parseFrontmatter(buf []byte) (skillFrontmatter, error) {
	// Strip an optional UTF-8 BOM so a file saved with one still parses.
	buf = bytes.TrimPrefix(buf, []byte{0xEF, 0xBB, 0xBF})
	if !bytes.HasPrefix(buf, []byte("---\n")) && !bytes.HasPrefix(buf, []byte("---\r\n")) {
		return skillFrontmatter{}, nil
	}
	// Advance past the opening fence.
	rest := buf[3:]
	if len(rest) > 0 && rest[0] == '\r' {
		rest = rest[1:]
	}
	if len(rest) > 0 && rest[0] == '\n' {
		rest = rest[1:]
	}
	// Find the closing fence — a line that is exactly `---`.
	end := -1
	for i := 0; i < len(rest); {
		lineEnd := len(rest)
		if nl := bytes.IndexByte(rest[i:], '\n'); nl >= 0 {
			lineEnd = i + nl
		}
		line := rest[i:lineEnd]
		line = bytes.TrimRight(line, "\r")
		if bytes.Equal(line, []byte("---")) {
			end = i
			break
		}
		if lineEnd == len(rest) {
			break
		}
		i = lineEnd + 1
	}
	if end < 0 {
		return skillFrontmatter{}, nil
	}
	var fm skillFrontmatter
	if err := yaml.Unmarshal(rest[:end], &fm); err != nil {
		return skillFrontmatter{}, fmt.Errorf("parse frontmatter: %w", err)
	}
	return fm, nil
}

// renderSkillCommand produces the `sh -c` snippet the client hands to
// ssh/mosh. Keeps the shape in lockstep with the old scaffolder run:
// cwd = ~/.drift, clear the handoff sentinel, exec claude with an
// auto-prefix that nudges it to invoke the named skill.
func renderSkillCommand(skill, prompt string) string {
	var initial strings.Builder
	initial.WriteString("Use the ")
	initial.WriteString(skill)
	initial.WriteString(" skill.")
	if prompt != "" {
		initial.WriteString(" ")
		initial.WriteString(prompt)
	}
	return `cd "$HOME/.drift" && rm -f last-scaffold && exec claude --dangerously-skip-permissions ` + shellQuote(initial.String())
}

// shellQuote mirrors internal/run/template.go's unexported shq — kept
// private here so the skill package stays self-contained.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
