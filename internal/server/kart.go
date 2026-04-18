package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
	yaml "gopkg.in/yaml.v3"
)

// KartDeps collects the collaborators the kart.* handlers need. It is
// separate from the main [Deps] bundle so Phase 7 can land without touching
// the Phase 6 handler wiring; both structs can be merged once their owners
// agree on a shape.
type KartDeps struct {
	// Devpod is the typed devpod wrapper. Required.
	Devpod *devpod.Client
	// GarageDir is the absolute path to `~/.drift/garage`. Kart handlers
	// reconcile workspaces listed by devpod against entries under
	// GarageDir/karts to detect stale karts (plans/PLAN.md § Stale karts).
	GarageDir string
}

// RegisterKart wires the kart.list and kart.info handlers into reg. Later
// phases append kart.new, kart.start, kart.stop, etc. from additional files
// in this package.
func RegisterKart(reg *rpc.Registry, d KartDeps) {
	reg.Register(wire.MethodKartList, d.kartListHandler)
	reg.Register(wire.MethodKartInfo, d.kartInfoHandler)
}

// KartConfig is the on-disk shape of `garage/karts/<name>/config.yaml`. The
// fields mirror plans/PLAN.md § Server state layout. Only the resource
// identifiers (tune, character, source) need to round-trip — container and
// devpod details come from devpod at query time.
type KartConfig struct {
	Repo       string `yaml:"repo,omitempty"`
	Tune       string `yaml:"tune,omitempty"`
	Character  string `yaml:"character,omitempty"`
	SourceMode string `yaml:"source_mode,omitempty"`
	User       string `yaml:"user,omitempty"`
	Shell      string `yaml:"shell,omitempty"`
	Image      string `yaml:"image,omitempty"`
	Workdir    string `yaml:"workdir,omitempty"`
	CreatedAt  string `yaml:"created_at,omitempty"`
}

// KartSource is the `source` sub-object of the kart.info payload.
// plans/PLAN.md § lakitu info kart — JSON schema.
type KartSource struct {
	Mode string `json:"mode"`
	URL  string `json:"url,omitempty"`
}

// KartContainer is the `container` sub-object. Absent (nil) when the kart
// is not running.
type KartContainer struct {
	User    string `json:"user,omitempty"`
	Shell   string `json:"shell,omitempty"`
	Workdir string `json:"workdir,omitempty"`
	Image   string `json:"image,omitempty"`
}

// KartDevpod is the `devpod` sub-object. Carries just enough to let a
// client correlate the kart back to its devpod workspace.
type KartDevpod struct {
	WorkspaceID string `json:"workspace_id"`
	Provider    string `json:"provider,omitempty"`
}

// KartInfo is the stable JSON shape returned by `kart.info` and embedded
// (per entry) in `kart.list`. Additive-only forward compat per plans/PLAN.md.
type KartInfo struct {
	Name      string         `json:"name"`
	Status    devpod.Status  `json:"status"`
	CreatedAt string         `json:"created_at,omitempty"`
	Source    KartSource     `json:"source"`
	Tune      string         `json:"tune,omitempty"`
	Character string         `json:"character"`
	Autostart bool           `json:"autostart"`
	Container *KartContainer `json:"container,omitempty"`
	Devpod    *KartDevpod    `json:"devpod,omitempty"`
	// Stale is true when the kart exists in the garage but devpod has no
	// matching workspace. plans/PLAN.md § Stale karts uses `status:error`
	// plus a `stale: true` hint in the list view; info returns the full
	// stale_kart error object instead of this shape.
	Stale bool `json:"stale,omitempty"`
}

// KartListResult is the envelope returned by `kart.list` — an object with a
// top-level `karts` array so additive fields (counts, garbage-collector
// hints, etc.) can be attached later without changing the array shape.
type KartListResult struct {
	Karts []KartInfo `json:"karts"`
}

// KartInfoParams is the request shape for `kart.info`.
type KartInfoParams struct {
	Name string `json:"name"`
}

// kartListHandler returns every kart known to the circuit — union of
// `devpod list` and `garage/karts/<name>/`. Entries that appear only in
// the garage get `status: error` + `stale: true`.
func (d KartDeps) kartListHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}

	workspaces, err := d.listWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	garage, err := d.listGarageKarts()
	if err != nil {
		return nil, err
	}

	wsByID := make(map[string]devpod.Workspace, len(workspaces))
	for _, w := range workspaces {
		wsByID[w.ID] = w
	}
	garageByName := make(map[string]KartConfig, len(garage))
	for name, cfg := range garage {
		garageByName[name] = cfg
	}

	// Union by name so a kart present in either system shows up exactly
	// once. Sort the resulting slice so the output is stable — clients
	// and testscripts compare strings.
	names := make(map[string]struct{}, len(workspaces)+len(garage))
	for _, w := range workspaces {
		names[w.ID] = struct{}{}
	}
	for name := range garage {
		names[name] = struct{}{}
	}
	ordered := make([]string, 0, len(names))
	for n := range names {
		ordered = append(ordered, n)
	}
	sort.Strings(ordered)

	karts := make([]KartInfo, 0, len(ordered))
	for _, name := range ordered {
		ws, inDevpod := wsByID[name]
		cfg, inGarage := garageByName[name]
		info := d.buildInfo(ctx, name, cfg, ws, inDevpod, inGarage)
		karts = append(karts, info)
	}
	return KartListResult{Karts: karts}, nil
}

// kartInfoHandler returns a single kart. A garage entry without a matching
// devpod workspace yields `stale_kart` (code 4) per plans/PLAN.md § Stale
// karts; an entirely unknown name yields `kart_not_found` (code 3).
func (d KartDeps) kartInfoHandler(ctx context.Context, params json.RawMessage) (any, error) {
	var p KartInfoParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "kart.info: name is required")
	}

	cfg, inGarage, err := d.readKartConfig(p.Name)
	if err != nil {
		return nil, err
	}
	workspaces, err := d.listWorkspaces(ctx)
	if err != nil {
		return nil, err
	}
	ws, inDevpod := findWorkspace(workspaces, p.Name)

	if !inGarage && !inDevpod {
		return nil, rpcerr.NotFound(rpcerr.TypeKartNotFound,
			"kart %q not found", p.Name).With("kart", p.Name)
	}
	if inGarage && !inDevpod {
		return nil, rpcerr.Conflict(rpcerr.TypeStaleKart,
			"kart %q is stale (garage state without devpod workspace)", p.Name).
			With("kart", p.Name).
			With("suggestion",
				fmt.Sprintf("drift delete %s to clean up, then drift new %s", p.Name, p.Name))
	}

	info := d.buildInfo(ctx, p.Name, cfg, ws, inDevpod, inGarage)
	return info, nil
}

// buildInfo assembles the KartInfo payload from the two data sources.
// Single place so the list and info handlers stay in sync — any future
// field gets added here and both callers pick it up for free.
func (d KartDeps) buildInfo(
	ctx context.Context,
	name string,
	cfg KartConfig,
	ws devpod.Workspace,
	inDevpod, inGarage bool,
) KartInfo {
	info := KartInfo{
		Name:      name,
		Status:    devpod.StatusNotFound,
		Character: cfg.Character,
		Tune:      cfg.Tune,
		Autostart: d.kartAutostartEnabled(name),
		Source:    sourceFromConfig(cfg, ws),
		CreatedAt: cfg.CreatedAt,
	}
	if !inDevpod && inGarage {
		info.Status = devpod.StatusError
		info.Stale = true
		return info
	}
	if inDevpod {
		info.Devpod = &KartDevpod{
			WorkspaceID: ws.ID,
			Provider:    ws.Provider.Name,
		}
		if info.CreatedAt == "" && ws.Created != "" {
			info.CreatedAt = ws.Created
		}
		info.Status = d.statusFor(ctx, name)
		if info.Status == devpod.StatusRunning {
			info.Container = containerFromConfig(cfg)
		}
	}
	return info
}

// statusFor fetches the runtime status of a workspace from devpod. Errors
// from the status call fold to StatusError — lakitu never surfaces a raw
// devpod exec failure in a list response; clients branch on the enum.
func (d KartDeps) statusFor(ctx context.Context, name string) devpod.Status {
	st, err := d.Devpod.Status(ctx, name)
	if err != nil {
		return devpod.StatusError
	}
	return st
}

// listWorkspaces wraps the devpod call and converts exec failures into a
// structured rpcerr so handlers can return it directly. Missing binary is
// treated as a devpod-unreachable condition rather than an internal error.
func (d KartDeps) listWorkspaces(ctx context.Context) ([]devpod.Workspace, error) {
	if d.Devpod == nil {
		return nil, rpcerr.Internal("kart: devpod client not configured")
	}
	workspaces, err := d.Devpod.List(ctx)
	if err != nil {
		return nil, rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUnreachable,
			"devpod list failed: %v", err).Wrap(err)
	}
	return workspaces, nil
}

// listGarageKarts enumerates `garage/karts/<name>/` directories. A missing
// karts/ directory yields an empty map, not an error — `lakitu init`
// creates it on a fresh garage, but the handler tolerates a user who hasn't
// run init yet so `kart.list` on a blank circuit is empty, not failing.
func (d KartDeps) listGarageKarts() (map[string]KartConfig, error) {
	root := filepath.Join(d.GarageDir, "karts")
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return map[string]KartConfig{}, nil
		}
		return nil, rpcerr.Internal("read %s: %v", root, err).Wrap(err)
	}
	out := make(map[string]KartConfig, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		cfg, loaded, err := d.readKartConfig(e.Name())
		if err != nil {
			return nil, err
		}
		if loaded {
			out[e.Name()] = cfg
		} else {
			// Directory without a config.yaml is still a garage-known
			// kart — surface it as stale rather than ignoring it.
			out[e.Name()] = KartConfig{}
		}
	}
	return out, nil
}

// readKartConfig loads garage/karts/<name>/config.yaml. The second return
// value is true when a config.yaml exists (the kart is garage-known);
// false+nil error means no garage entry at all.
func (d KartDeps) readKartConfig(name string) (KartConfig, bool, error) {
	path := filepath.Join(d.GarageDir, "karts", name, "config.yaml")
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			dir := filepath.Join(d.GarageDir, "karts", name)
			if _, derr := os.Stat(dir); derr == nil {
				// Dir exists but config.yaml doesn't — treat as garage-known
				// with zero config so stale detection still fires.
				return KartConfig{}, true, nil
			}
			return KartConfig{}, false, nil
		}
		return KartConfig{}, false, rpcerr.Internal("read %s: %v", path, err).Wrap(err)
	}
	var cfg KartConfig
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return KartConfig{}, false, rpcerr.Internal("parse %s: %v", path, err).Wrap(err)
	}
	return cfg, true, nil
}

// kartAutostartEnabled reports whether the marker file in the kart's
// garage dir is present (plans/PLAN.md § Server state layout).
func (d KartDeps) kartAutostartEnabled(name string) bool {
	path := filepath.Join(d.GarageDir, "karts", name, "autostart")
	if _, err := os.Stat(path); err == nil {
		return true
	}
	return false
}

func findWorkspace(workspaces []devpod.Workspace, name string) (devpod.Workspace, bool) {
	for _, w := range workspaces {
		if w.ID == name {
			return w, true
		}
	}
	return devpod.Workspace{}, false
}

// sourceFromConfig builds the KartSource sub-object. The garage config is
// authoritative for mode (set at kart creation time). If the garage has no
// opinion, fall back to the devpod workspace's Source.
func sourceFromConfig(cfg KartConfig, ws devpod.Workspace) KartSource {
	mode := cfg.SourceMode
	url := cfg.Repo
	if mode == "" {
		switch {
		case ws.Source.GitRepository != "":
			mode = "clone"
			url = ws.Source.GitRepository
		case ws.Source.LocalFolder != "":
			mode = "starter"
			url = ws.Source.LocalFolder
		default:
			mode = "none"
		}
	}
	src := KartSource{Mode: mode}
	if mode != "none" {
		src.URL = url
	}
	return src
}

// containerFromConfig returns the kart-creation-time container hints. Nil
// when the garage has no opinion (a running kart with no config is a rare
// transient state — callers should tolerate a missing `container` field).
func containerFromConfig(cfg KartConfig) *KartContainer {
	if cfg.User == "" && cfg.Shell == "" && cfg.Workdir == "" && cfg.Image == "" {
		return nil
	}
	return &KartContainer{
		User:    cfg.User,
		Shell:   cfg.Shell,
		Workdir: cfg.Workdir,
		Image:   cfg.Image,
	}
}
