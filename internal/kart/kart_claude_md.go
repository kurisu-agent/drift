package kart

import (
	"bytes"
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"

	"github.com/tailscale/hujson"

	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/model"
)

//go:embed kart_claude_md.tmpl
var kartClaudeMDTemplate string

var kartClaudeMDTmpl = template.Must(template.New("kart_claude_md").Parse(kartClaudeMDTemplate))

// hasClaudeStateMount reports whether the kart's mounts share any
// `~/.claude` state from the workstation — the signal for "this kart
// runs claude agents" and thus should get a drop-in CLAUDE.md.
func hasClaudeStateMount(mounts []model.Mount) bool {
	for _, m := range mounts {
		suffix, ok := targetInHome(m.Target)
		if !ok {
			continue
		}
		if suffix == ".claude" || strings.HasPrefix(suffix, ".claude/") {
			return true
		}
	}
	return false
}

type kartClaudeMDData struct {
	Image            string
	DevcontainerPath string
}

// claudeMDFragment returns the shell snippet that drops the
// orientation CLAUDE.md at $HOME/.claude/CLAUDE.md. Empty when no
// ~/.claude state is shared (so agents aren't surprised by a file
// they didn't ask for).
func claudeMDFragment(r *Resolved) (string, error) {
	if r == nil || r.Name == "" || !hasClaudeStateMount(r.Mounts) {
		return "", nil
	}
	body, err := renderKartClaudeMD(r)
	if err != nil {
		return "", err
	}
	if body == "" {
		return "", nil
	}
	// If the target is an inherited symlink, break it so we write to
	// a local file rather than the host-side bind source.
	var b strings.Builder
	b.WriteString(`mkdir -p "$HOME/.claude"` + "\n")
	b.WriteString(`dst="$HOME/.claude/CLAUDE.md"` + "\n")
	b.WriteString(`if [ -L "$dst" ]; then rm -f "$dst"; fi` + "\n")
	b.WriteString(base64WriteStmt(`$HOME/.claude/CLAUDE.md`, []byte(body)))
	return b.String(), nil
}

func renderKartClaudeMD(r *Resolved) (string, error) {
	image, relPath := probeProjectDevcontainer(r.Name)
	if relPath == "" {
		relPath = ".devcontainer/devcontainer.json"
	}
	data := kartClaudeMDData{
		Image:            image,
		DevcontainerPath: "/workspaces/" + r.Name + "/" + relPath,
	}
	var buf bytes.Buffer
	if err := kartClaudeMDTmpl.Execute(&buf, data); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// probeProjectDevcontainer reads the cloned repo's devcontainer.json
// (looking in the two standard locations) and extracts the image
// field. Best-effort: any missing file or parse failure returns
// empty strings, which the template handles as "unknown".
func probeProjectDevcontainer(kart string) (image, relPath string) {
	dir := filepath.Join(devpod.AgentContextsRoot(), "default", "workspaces", kart, "content")
	for _, candidate := range []string{".devcontainer/devcontainer.json", ".devcontainer.json"} {
		raw, err := os.ReadFile(filepath.Join(dir, candidate))
		if err != nil {
			continue
		}
		standard, err := hujson.Standardize(raw)
		if err != nil {
			return "", candidate
		}
		var dc struct {
			Image string `json:"image"`
		}
		if err := json.Unmarshal(standard, &dc); err != nil {
			return "", candidate
		}
		return dc.Image, candidate
	}
	return "", ""
}
