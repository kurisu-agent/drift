package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"gopkg.in/yaml.v3"
)

// DriftCheckParams carries the kart name.
type DriftCheckParams struct {
	Name string `json:"name"`
}

// DriftedField captures one structural diff between the captured
// kart config and the live tune. Source is "tune" or "character";
// Path is the dotted field path (e.g. "env.build.GITHUB_TOKEN" or
// "mount_dirs"); Was/Now are the on-disk values pre-redaction.
type DriftedField struct {
	Source     string `json:"source"`
	SourceName string `json:"source_name,omitempty"`
	Path       string `json:"path"`
	Was        any    `json:"was,omitempty"`
	Now        any    `json:"now,omitempty"`
}

// DriftCheckResult: Drifted true iff any structural field differs.
// Fields lists them in stable order so the client renders a
// deterministic summary.
type DriftCheckResult struct {
	Name    string         `json:"name"`
	Drifted bool           `json:"drifted"`
	Fields  []DriftedField `json:"fields,omitempty"`
}

// RebuildParams carries the kart name.
type RebuildParams struct {
	Name string `json:"name"`
}

// RebuildResult mirrors KartLifecycleResult for consistency.
type RebuildResult struct {
	Name   string        `json:"name"`
	Status devpod.Status `json:"status"`
}

// kartDriftCheckHandler reports structural drift between the
// captured kart config (garage/karts/<name>/config.yaml) and the
// current tune at garage/tunes/<tune>.yaml. Today compares:
//
//   - tune.env.build.*         (chest refs on the tune's build env)
//   - tune.mount_dirs          (host binds)
//
// Other structural fields (tune.devcontainer, tune.features,
// tune.dotfiles_repo, character.ssh_key_path, character.pat_secret)
// aren't captured on the kart config today and require a snapshot
// field to compare — follow-up.
func (d KartDeps) kartDriftCheckHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p DriftCheckParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "kart.drift_check: name is required")
	}
	cfg, ok, err := d.readKartConfig(p.Name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, rpcerr.NotFound(rpcerr.TypeKartNotFound,
			"kart %q not found", p.Name).With("kart", p.Name)
	}
	// A kart with no bound tune can't drift by definition.
	if cfg.Tune == "" {
		return DriftCheckResult{Name: p.Name, Drifted: false}, nil
	}
	deps := &Deps{GarageDir: d.GarageDir}
	tune, err := deps.loadTune(cfg.Tune)
	if err != nil {
		// Tune removal counts as drift — flag the missing source so the
		// client can prompt the user to pick a new tune before rebuild.
		var rerr *rpcerr.Error
		if errors.As(err, &rerr) && rerr.Type == typeTuneNotFound {
			return DriftCheckResult{
				Name:    p.Name,
				Drifted: true,
				Fields: []DriftedField{{
					Source:     "tune",
					SourceName: cfg.Tune,
					Path:       "<tune-missing>",
					Was:        "present",
					Now:        "not-found",
				}},
			}, nil
		}
		return nil, err
	}
	fields := diffTuneStructural(cfg, tune)
	sort.Slice(fields, func(i, j int) bool { return fields[i].Path < fields[j].Path })
	// Tag source for each diff.
	for i := range fields {
		fields[i].Source = "tune"
		fields[i].SourceName = cfg.Tune
	}
	return DriftCheckResult{
		Name:    p.Name,
		Drifted: len(fields) > 0,
		Fields:  fields,
	}, nil
}

// diffTuneStructural compares the kart's captured env+mount_dirs
// (the only bits recorded at kart.new time today) against the
// live tune. Missing captured entries fall back to the zero value,
// so a freshly-set env var shows up as "was:nil → now:chest:…".
func diffTuneStructural(cfg KartConfig, tune *Tune) []DriftedField {
	var out []DriftedField
	// env.build: the original bug's field. Compare map equality.
	capBuild := cfg.Env.Build
	liveBuild := tune.Env.Build
	if !mapEqual(capBuild, liveBuild) {
		for _, k := range mergedKeys(capBuild, liveBuild) {
			was := capBuild[k]
			now := liveBuild[k]
			if was == now {
				continue
			}
			out = append(out, DriftedField{
				Path: "env.build." + k,
				Was:  valueOrNil(was),
				Now:  valueOrNil(now),
			})
		}
	}
	// mount_dirs: capture array vs live array. Treat as drift when
	// non-equal by target+source pair (order doesn't matter, dupes
	// are flagged).
	if !mountsEqual(cfg.MountDirs, tune.MountDirs) {
		out = append(out, DriftedField{
			Path: "mount_dirs",
			Was:  mountSummary(cfg.MountDirs),
			Now:  mountSummary(tune.MountDirs),
		})
	}
	return out
}

func mapEqual(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, va := range a {
		if vb, ok := b[k]; !ok || va != vb {
			return false
		}
	}
	return true
}

// mergedKeys returns the union of keys in two maps, sorted.
func mergedKeys(a, b map[string]string) []string {
	seen := make(map[string]struct{}, len(a)+len(b))
	for k := range a {
		seen[k] = struct{}{}
	}
	for k := range b {
		seen[k] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for k := range seen {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func valueOrNil(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// mountsEqual: treat mount lists as equal when their normalised
// shape matches ignoring order. Two mounts are equal iff all
// fields match by value.
func mountsEqual(a, b []model.Mount) bool {
	if len(a) != len(b) {
		return false
	}
	sa := append([]model.Mount(nil), a...)
	sb := append([]model.Mount(nil), b...)
	sort.Slice(sa, func(i, j int) bool { return mountKey(sa[i]) < mountKey(sa[j]) })
	sort.Slice(sb, func(i, j int) bool { return mountKey(sb[i]) < mountKey(sb[j]) })
	return reflect.DeepEqual(sa, sb)
}

func mountKey(m model.Mount) string {
	return m.Type + "|" + m.Source + "|" + m.Target
}

// mountSummary renders a short "type src→target" line for each
// mount, safe to print in the rendered drift table.
func mountSummary(ms []model.Mount) []string {
	if len(ms) == 0 {
		return nil
	}
	out := make([]string, 0, len(ms))
	for _, m := range ms {
		out = append(out, fmt.Sprintf("%s %s→%s", m.Type, m.Source, m.Target))
	}
	return out
}

// kartRebuildHandler recreates a kart's container against the
// current tune + character shape. Intended to be invoked after
// kart.drift_check reports drift and the user consents. Under the
// hood: rewrites garage/karts/<name>/config.yaml from the current
// tune, then runs `devpod up --recreate` so the container picks up
// new mount_dirs / env / devcontainer config.
//
// Caveat: this rebuild only refreshes the captured env+mount_dirs
// from the current tune — fuller reprovisioning (re-cloning, starter
// strip) still lives in kart.new. A separate `--deep` knob is a
// follow-up.
func (d KartDeps) kartRebuildHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p RebuildParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "kart.rebuild: name is required")
	}
	if err := d.requireDevpod(); err != nil {
		return nil, err
	}
	cfg, ok, err := d.readKartConfig(p.Name)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, rpcerr.NotFound(rpcerr.TypeKartNotFound,
			"kart %q not found", p.Name).With("kart", p.Name)
	}

	// Re-snapshot env+mount_dirs from the current tune into the kart
	// config, so next lifecycle ops use the up-to-date shape.
	if cfg.Tune != "" {
		deps := &Deps{GarageDir: d.GarageDir}
		tune, terr := deps.loadTune(cfg.Tune)
		if terr == nil {
			cfg.Env = tune.Env
			cfg.MountDirs = append([]model.Mount(nil), tune.MountDirs...)
			if err := d.writeKartConfigYAML(p.Name, cfg); err != nil {
				return nil, err
			}
		}
	}

	// Resolve workspace env from the (possibly refreshed) chest refs so
	// the recreate picks up new values on the next up.
	wsEnv, err := d.workspaceEnvKVs(p.Name)
	if err != nil {
		return nil, err
	}
	// devpod up --recreate rebuilds the container image and recreates
	// the container itself — what we need for env.build / mount_dirs
	// drift to take effect.
	opts := devpod.UpOpts{Name: p.Name, WorkspaceEnv: wsEnv, Recreate: true}
	if _, err := d.Devpod.Up(ctx, opts); err != nil {
		return nil, wrapDevpod(rpcerr.CodeDevpod, rpcerr.TypeDevpodUpFailed, p.Name, err,
			"devpod up --recreate %s failed: %v", p.Name, err)
	}
	return RebuildResult{Name: p.Name, Status: d.statusFor(ctx, p.Name)}, nil
}

// writeKartConfigYAML marshals and atomic-writes a kart config.
// Mirrors the internal writeKartConfig in internal/kart but takes
// a post-modify KartConfig directly so the rebuild path can stamp
// refreshed env+mount_dirs without dragging in the resolver.
func (d KartDeps) writeKartConfigYAML(name string, cfg KartConfig) error {
	path := config.KartConfigPath(d.GarageDir, name)
	buf, err := yaml.Marshal(&cfg)
	if err != nil {
		return rpcerr.Internal("kart.rebuild: marshal: %v", err).Wrap(err)
	}
	if err := config.WriteFileAtomic(path, buf, 0o644); err != nil {
		return rpcerr.Internal("kart.rebuild: write %s: %v", path, err).Wrap(err)
	}
	return nil
}
