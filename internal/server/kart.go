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
	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
	yaml "gopkg.in/yaml.v3"
)

type KartDeps struct {
	Devpod    *devpod.Client
	GarageDir string
}

func RegisterKart(reg *rpc.Registry, d KartDeps) {
	reg.Register(wire.MethodKartList, d.kartListHandler)
	reg.Register(wire.MethodKartInfo, d.kartInfoHandler)
}

// KartConfig is the on-disk shape of garage/karts/<name>/config.yaml. Only
// identifiers (tune, character, source) round-trip — runtime details come
// from devpod at query time.
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

// KartSource is aliased to model.KartSource so server and kart packages
// share one type while existing server.KartSource callers still compile.
type KartSource = model.KartSource

// KartContainer is absent (nil) when the kart is not running.
type KartContainer struct {
	User    string `json:"user,omitempty"`
	Shell   string `json:"shell,omitempty"`
	Workdir string `json:"workdir,omitempty"`
	Image   string `json:"image,omitempty"`
}

type KartDevpod struct {
	WorkspaceID string `json:"workspace_id"`
	Provider    string `json:"provider,omitempty"`
}

// KartInfo is the stable JSON shape returned by kart.info and embedded
// (per entry) in kart.list. Additive-only forward compat.
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
	// Stale: garage-known without a matching devpod workspace. List surfaces
	// `status:error` + `stale:true`; info returns a stale_kart error instead.
	Stale bool `json:"stale,omitempty"`
}

// KartListResult is wrapped in an object so additive top-level fields
// (counts, GC hints) can be added without changing the array shape.
type KartListResult struct {
	Karts []KartInfo `json:"karts"`
}

type KartInfoParams struct {
	Name string `json:"name"`
}

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

	// Union by name so a kart present in either system shows up once;
	// sorted so output is stable for testscript diffs.
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

// buildInfo is the single place that assembles KartInfo so list and info
// stay in sync.
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

// statusFor folds devpod status errors to StatusError — lakitu never leaks
// a raw devpod exec failure in a list response.
func (d KartDeps) statusFor(ctx context.Context, name string) devpod.Status {
	st, err := d.Devpod.Status(ctx, name)
	if err != nil {
		return devpod.StatusError
	}
	return st
}

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

// listGarageKarts tolerates a missing karts/ dir — returns an empty map
// rather than erroring on a circuit that hasn't run `lakitu init` yet.
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
			// Dir without config.yaml is still garage-known — surface as
			// stale rather than ignoring.
			out[e.Name()] = KartConfig{}
		}
	}
	return out, nil
}

// readKartConfig returns (cfg, true, nil) when the kart is garage-known and
// (_, false, nil) when there's no garage entry at all.
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

// sourceFromConfig: garage config is authoritative for mode. If garage has
// no opinion, fall back to the devpod workspace's Source.
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
