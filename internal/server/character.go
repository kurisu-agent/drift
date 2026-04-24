package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/kurisu-agent/drift/internal/chest"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/yamlpath"
	"gopkg.in/yaml.v3"
)

// Character: git_name + git_email required. pat_secret always carries a
// `chest:<name>` reference — literal tokens are rejected at new/patch time.
type Character struct {
	GitName    string `yaml:"git_name" json:"git_name"`
	GitEmail   string `yaml:"git_email" json:"git_email"`
	GithubUser string `yaml:"github_user,omitempty" json:"github_user,omitempty"`
	SSHKeyPath string `yaml:"ssh_key_path,omitempty" json:"ssh_key_path,omitempty"`
	PATSecret  string `yaml:"pat_secret,omitempty" json:"pat_secret,omitempty"`
}

// CharacterNewParams carries the full shape at creation time. git_name
// and git_email are required; the rest are optional.
type CharacterNewParams struct {
	Name       string `json:"name"`
	GitName    string `json:"git_name"`
	GitEmail   string `json:"git_email"`
	GithubUser string `json:"github_user,omitempty"`
	SSHKeyPath string `json:"ssh_key_path,omitempty"`
	PATSecret  string `json:"pat_secret,omitempty"`
}

// CharacterPatchOp / CharacterPatchParams: same shape as tune.patch —
// dotted-path field addressing.
type CharacterPatchOp struct {
	Path  string `json:"path"`
	Op    string `json:"op"`
	Value any    `json:"value,omitempty"`
}

type CharacterPatchParams struct {
	Name string             `json:"name"`
	Ops  []CharacterPatchOp `json:"ops"`
}

type CharacterReplaceParams struct {
	Name string `json:"name"`
	YAML string `json:"yaml"`
}

// CharacterResult bundles the name with the character yaml for easy
// table rendering on the client.
type CharacterResult struct {
	Name string `json:"name"`
	Character
}

type CharacterNameOnly struct {
	Name string `json:"name"`
}

// CharacterNewHandler creates a character. Errors if one with the
// same name exists — edits go through character.patch or
// character.replace.
func (d *Deps) CharacterNewHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p CharacterNewParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if err := name.ValidateCharacter(p.Name); err != nil {
		return nil, err
	}
	if p.GitName == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "character.new: git_name is required")
	}
	if p.GitEmail == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "character.new: git_email is required")
	}
	if p.PATSecret != "" && !strings.HasPrefix(p.PATSecret, chest.RefPrefix) {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"character.new: pat_secret must be a chest reference of the form %q; literal tokens are not accepted",
			chest.RefPrefix+"<name>").With("pat_secret", "redacted")
	}

	path := d.characterPath(p.Name)
	if _, err := os.Stat(path); err == nil {
		return nil, rpcerr.Conflict(rpcerr.TypeNameCollision,
			"character %q already exists — use character.patch or character.replace to edit", p.Name).With("name", p.Name)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, rpcerr.Internal("character.new: stat %s: %v", path, err).Wrap(err)
	}

	c := Character{
		GitName:    p.GitName,
		GitEmail:   p.GitEmail,
		GithubUser: p.GithubUser,
		SSHKeyPath: p.SSHKeyPath,
		PATSecret:  p.PATSecret,
	}
	if err := writeCharacter(path, &c); err != nil {
		return nil, err
	}
	return CharacterResult{Name: p.Name, Character: c}, nil
}

// CharacterPatchHandler applies ops to an existing character.
func (d *Deps) CharacterPatchHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p CharacterPatchParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "character.patch: name is required")
	}
	c, err := d.loadCharacter(p.Name)
	if err != nil {
		return nil, err
	}
	ops := make([]yamlpath.Op, 0, len(p.Ops))
	for _, op := range p.Ops {
		ops = append(ops, yamlpath.Op{Path: op.Path, Op: op.Op, Value: op.Value})
	}
	if err := yamlpath.Apply(c, ops); err != nil {
		return nil, wrapYAMLPathError("character.patch", err)
	}
	if err := validateCharacter(c); err != nil {
		return nil, err
	}
	if err := writeCharacter(d.characterPath(p.Name), c); err != nil {
		return nil, err
	}
	return CharacterResult{Name: p.Name, Character: *c}, nil
}

// CharacterReplaceHandler: full-YAML round-trip for the edit flow.
func (d *Deps) CharacterReplaceHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p CharacterReplaceParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "character.replace: name is required")
	}
	if _, err := os.Stat(d.characterPath(p.Name)); err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, rpcerr.NotFound(rpcerr.TypeCharacterNotFound,
				"character %q not found — use character.new to create", p.Name).With("name", p.Name)
		}
		return nil, rpcerr.Internal("character.replace: stat: %v", err).Wrap(err)
	}
	var c Character
	dec := yaml.NewDecoder(strings.NewReader(p.YAML))
	dec.KnownFields(true)
	if err := dec.Decode(&c); err != nil && !errors.Is(err, io.EOF) {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"character.replace: invalid YAML: %v", err).With("name", p.Name)
	}
	if err := validateCharacter(&c); err != nil {
		return nil, err
	}
	if err := writeCharacter(d.characterPath(p.Name), &c); err != nil {
		return nil, err
	}
	return CharacterResult{Name: p.Name, Character: c}, nil
}

// CharacterListHandler surfaces the full yaml (incl. pat_secret) so
// `--output json` is useful; the CLI picks columns for humans.
func (d *Deps) CharacterListHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	names, err := listYAMLNames(d.characterDir())
	if err != nil {
		return nil, rpcerr.Internal("character.list: %v", err).Wrap(err)
	}
	out := make([]CharacterResult, 0, len(names))
	for _, n := range names {
		c, err := d.loadCharacter(n)
		if err != nil {
			return nil, err
		}
		out = append(out, CharacterResult{Name: n, Character: *c})
	}
	return out, nil
}

// CharacterShowHandler: pat_secret surfaced verbatim — it's a chest
// reference, never a literal secret.
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

// CharacterRemoveHandler rejects removal when any kart references the
// character — per-kart config is the only place a character is pinned.
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

func writeCharacter(path string, c *Character) error {
	buf, err := yaml.Marshal(c)
	if err != nil {
		return rpcerr.Internal("character: marshal: %v", err).Wrap(err)
	}
	if err := config.WriteFileAtomic(path, buf, 0o644); err != nil {
		return rpcerr.Internal("character: %v", err).Wrap(err)
	}
	return nil
}

// validateCharacter enforces the required-fields and chest-ref
// invariants on the full character shape. Used by patch and replace
// after the mutation is applied — new-time validation is inlined in
// the new handler to surface errors against the specific CLI flag.
func validateCharacter(c *Character) error {
	if c.GitName == "" {
		return rpcerr.UserError(rpcerr.TypeInvalidFlag, "character: git_name is required")
	}
	if c.GitEmail == "" {
		return rpcerr.UserError(rpcerr.TypeInvalidFlag, "character: git_email is required")
	}
	if c.PATSecret != "" && !strings.HasPrefix(c.PATSecret, chest.RefPrefix) {
		return rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"character: pat_secret must be a chest reference of the form %q; literal tokens are not accepted",
			chest.RefPrefix+"<name>").With("pat_secret", "redacted")
	}
	return nil
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

// kartsReferencing is tolerant — reads only the fields we care about —
// so a malformed kart config doesn't block character/tune removal.
func (d *Deps) kartsReferencing(field, value string) ([]string, error) {
	g, _ := d.garageDir()
	kartsDir := config.KartsDir(g)
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
		cfg := config.KartConfigPath(g, e.Name())
		buf, err := os.ReadFile(cfg)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, rpcerr.Internal("karts: read %s: %v", cfg, err).Wrap(err)
		}
		// Permissive map — we survive additive changes to the per-kart
		// config schema.
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
