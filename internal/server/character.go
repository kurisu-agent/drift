package server

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"gopkg.in/yaml.v3"
)

// Character is the on-disk schema for `characters/<name>.yaml`. Only
// git_name and git_email are required; `pat_secret` always carries a
// `chest:<name>` reference (literal tokens are rejected at add time).
//
// Mirrors plans/PLAN.md § Character file.
type Character struct {
	GitName    string `yaml:"git_name" json:"git_name"`
	GitEmail   string `yaml:"git_email" json:"git_email"`
	GithubUser string `yaml:"github_user,omitempty" json:"github_user,omitempty"`
	SSHKeyPath string `yaml:"ssh_key_path,omitempty" json:"ssh_key_path,omitempty"`
	PATSecret  string `yaml:"pat_secret,omitempty" json:"pat_secret,omitempty"`
}

// CharacterAddParams is the RPC param shape for `character.add`.
type CharacterAddParams struct {
	Name       string `json:"name"`
	GitName    string `json:"git_name"`
	GitEmail   string `json:"git_email"`
	GithubUser string `json:"github_user,omitempty"`
	SSHKeyPath string `json:"ssh_key_path,omitempty"`
	PATSecret  string `json:"pat_secret,omitempty"`
}

// CharacterResult is returned by add/show — the on-disk character plus its
// name for easy table rendering on the client.
type CharacterResult struct {
	Name string `json:"name"`
	Character
}

// CharacterNameOnly is the param shape for show/remove.
type CharacterNameOnly struct {
	Name string `json:"name"`
}

const chestRefPrefix = "chest:"

// CharacterAddHandler writes a new character yaml. A name collision is a
// `code:4 name_collision` error — `character.add` is create-only; edits go
// through `character.remove` + `character.add` for now.
func (d *Deps) CharacterAddHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p CharacterAddParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if err := name.Validate("character", p.Name); err != nil {
		return nil, err
	}
	if p.GitName == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "character.add: git_name is required")
	}
	if p.GitEmail == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "character.add: git_email is required")
	}
	if p.PATSecret != "" && !strings.HasPrefix(p.PATSecret, chestRefPrefix) {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"character.add: pat_secret must be a chest reference of the form %q; literal tokens are not accepted",
			chestRefPrefix+"<name>").With("pat_secret", "redacted")
	}

	path := d.characterPath(p.Name)
	if _, err := os.Stat(path); err == nil {
		return nil, rpcerr.Conflict(rpcerr.TypeNameCollision,
			"character %q already exists", p.Name).With("name", p.Name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, rpcerr.Internal("character.add: stat %s: %v", path, err).Wrap(err)
	}

	c := Character{
		GitName:    p.GitName,
		GitEmail:   p.GitEmail,
		GithubUser: p.GithubUser,
		SSHKeyPath: p.SSHKeyPath,
		PATSecret:  p.PATSecret,
	}
	buf, err := yaml.Marshal(&c)
	if err != nil {
		return nil, rpcerr.Internal("character.add: marshal: %v", err).Wrap(err)
	}
	if err := config.WriteFileAtomic(path, buf, 0o644); err != nil {
		return nil, rpcerr.Internal("character.add: %v", err).Wrap(err)
	}
	return CharacterResult{Name: p.Name, Character: c}, nil
}

// CharacterListHandler enumerates `garage/characters/*.yaml` and returns a
// summary record per file. The full character file (including pat_secret)
// is surfaced so `drift character list --output json` is useful; human
// rendering on the CLI side can choose which columns to show.
func (d *Deps) CharacterListHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	dir := d.characterDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return []CharacterResult{}, nil
		}
		return nil, rpcerr.Internal("character.list: %v", err).Wrap(err)
	}
	out := make([]CharacterResult, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		n := strings.TrimSuffix(e.Name(), ".yaml")
		c, err := d.loadCharacter(n)
		if err != nil {
			return nil, err
		}
		out = append(out, CharacterResult{Name: n, Character: *c})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// CharacterShowHandler returns a single character. pat_secret is surfaced
// verbatim — it's a chest reference, never a literal secret.
func (d *Deps) CharacterShowHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p CharacterNameOnly
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "character.show: name is required")
	}
	c, err := d.loadCharacter(p.Name)
	if err != nil {
		return nil, err
	}
	return CharacterResult{Name: p.Name, Character: *c}, nil
}

// CharacterRemoveHandler deletes a character yaml. Removal is rejected if
// any kart references the character — scanning `garage/karts/*/config.yaml`
// is sufficient because per-kart config is the only place a character is
// pinned (plans/COMMANDS.md § drift character).
func (d *Deps) CharacterRemoveHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p CharacterNameOnly
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "character.remove: name is required")
	}
	path := d.characterPath(p.Name)
	if _, err := os.Stat(path); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, rpcerr.NotFound(rpcerr.TypeCharacterNotFound,
				"character %q not found", p.Name).With("name", p.Name)
		}
		return nil, rpcerr.Internal("character.remove: stat %s: %v", path, err).Wrap(err)
	}
	used, err := d.kartsReferencing("character", p.Name)
	if err != nil {
		return nil, err
	}
	if len(used) > 0 {
		return nil, rpcerr.Conflict(rpcerr.TypeNameCollision,
			"character %q is referenced by kart(s): %s", p.Name, strings.Join(used, ", ")).
			With("name", p.Name).With("karts", used)
	}
	if err := os.Remove(path); err != nil {
		return nil, rpcerr.Internal("character.remove: %v", err).Wrap(err)
	}
	return CharacterNameOnly{Name: p.Name}, nil
}

func (d *Deps) characterDir() string {
	g, _ := d.garageDir()
	return filepath.Join(g, "characters")
}

func (d *Deps) characterPath(n string) string {
	return filepath.Join(d.characterDir(), n+".yaml")
}

func (d *Deps) loadCharacter(n string) (*Character, error) {
	path := d.characterPath(n)
	buf, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, rpcerr.NotFound(rpcerr.TypeCharacterNotFound,
				"character %q not found", n).With("name", n)
		}
		return nil, rpcerr.Internal("character: %v", err).Wrap(err)
	}
	var c Character
	if err := yaml.Unmarshal(buf, &c); err != nil {
		return nil, rpcerr.Internal("character: decode %s: %v", path, err).Wrap(err)
	}
	return &c, nil
}

// kartsReferencing returns the names of karts whose config.yaml has
// `<field>: <value>`. The scan is deliberately tolerant — we only read the
// fields we care about — so a malformed kart config doesn't prevent
// character/tune removal elsewhere.
func (d *Deps) kartsReferencing(field, value string) ([]string, error) {
	g, _ := d.garageDir()
	kartsDir := filepath.Join(g, "karts")
	entries, err := os.ReadDir(kartsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, rpcerr.Internal("karts: %v", err).Wrap(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cfg := filepath.Join(kartsDir, e.Name(), "config.yaml")
		buf, err := os.ReadFile(cfg)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, rpcerr.Internal("karts: read %s: %v", cfg, err).Wrap(err)
		}
		// The per-kart config is owned by Phase 8; we don't have a typed
		// schema yet. Use a permissive map so we survive additive changes.
		var raw map[string]any
		if err := yaml.Unmarshal(buf, &raw); err != nil {
			return nil, rpcerr.Internal("karts: decode %s: %v", cfg, err).Wrap(err)
		}
		if v, ok := raw[field].(string); ok && v == value {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out, nil
}

