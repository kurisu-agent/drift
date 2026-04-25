package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"

	"github.com/kurisu-agent/drift/internal/chest"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/docker"
	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
	yaml "gopkg.in/yaml.v3"
)

// devcontainerIDLabel is the docker label devpod's devcontainer
// integration sets to the workspace UID. Lakitu reads it back via
// `docker ps` to fan in container state for every workspace in one
// round-trip — see KartDeps.Docker and BuildKartList.
const devcontainerIDLabel = "dev.containers.id"

type KartDeps struct {
	Devpod *devpod.Client
	// Docker queries container state for every workspace UID in a single
	// `docker ps` so kart.list and server.status answer in O(1) instead
	// of O(N) `devpod status` shells. Optional only in tests — production
	// always wires this.
	Docker    *docker.Client
	GarageDir string
	// OpenChest lets the lifecycle handlers re-resolve persisted env refs
	// on start/restart. Production binds this to server.Deps.openChest so
	// rotated secrets land on the next re-up. nil means "no chest" — the
	// workspace env stays empty even when the kart config names refs.
	OpenChest func() (chest.Backend, error)
}

func RegisterKart(reg *rpc.Registry, d KartDeps) {
	reg.Register(wire.MethodKartList, d.kartListHandler)
	reg.Register(wire.MethodKartInfo, d.kartInfoHandler)
}

// KartConfig is aliased to model.KartConfig so server and kart packages
// share one on-disk type while existing server.KartConfig callers (tests,
// CLI glue) still compile. The wire-format (YAML tags) lives on the
// canonical type in internal/model.
type KartConfig = model.KartConfig

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
	LastUsed  string         `json:"last_used,omitempty"`
	Source    KartSource     `json:"source"`
	Tune      string         `json:"tune,omitempty"`
	Character string         `json:"character"`
	Autostart bool           `json:"autostart"`
	Container *KartContainer `json:"container,omitempty"`
	Devpod    *KartDevpod    `json:"devpod,omitempty"`
	// Env surfaces the chest references (never values) for each injection
	// site; omitted entirely when no env is configured. Present-but-empty
	// blocks are not emitted — keeps the info JSON tight.
	Env *KartInfoEnv `json:"env,omitempty"`
	// Stale: garage-known without a matching devpod workspace. List surfaces
	// `status:error` + `stale:true`; info returns a stale_kart error instead.
	Stale bool `json:"stale,omitempty"`
}

// KartInfoEnv groups persisted env refs by injection site for `kart info`.
// Values are never rendered — only the chest reference per key.
type KartInfoEnv struct {
	Build     map[string]string `json:"build,omitempty"`
	Workspace map[string]string `json:"workspace,omitempty"`
	Session   map[string]string `json:"session,omitempty"`
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
	karts, err := d.BuildKartList(ctx)
	if err != nil {
		return nil, err
	}
	return KartListResult{Karts: karts}, nil
}

// BuildKartList assembles the merged garage+devpod kart roster with
// container state already filled in. Exported so server.status can fold
// the same data into its single-round-trip response without duplicating
// the merge logic.
//
// Status is resolved in one `docker ps` call (see KartDeps.Docker),
// keyed off each workspace's UID. When Docker is unset (test paths), we
// fall back to per-kart `devpod status` calls so existing fakes keep
// working — production always wires Docker.
func (d KartDeps) BuildKartList(ctx context.Context) ([]KartInfo, error) {
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

	// One `docker ps` covers every workspace's container state. Falling
	// out of this path before iterating karts means the per-kart status
	// branch can be a pure map lookup.
	statusByUID, err := d.dockerStatusMap(ctx)
	if err != nil {
		return nil, err
	}

	karts := make([]KartInfo, len(ordered))
	for i, name := range ordered {
		ws, inDevpod := wsByID[name]
		cfg, inGarage := garage[name]
		karts[i] = d.buildInfo(ctx, name, cfg, ws, inDevpod, inGarage, statusByUID)
	}
	return karts, nil
}

// dockerStatusMap returns a UID→devpod.Status map sourced from one
// `docker ps`. Returns nil (the "fall back to devpod status" sentinel)
// when KartDeps.Docker isn't wired — only test paths take that branch.
func (d KartDeps) dockerStatusMap(ctx context.Context) (map[string]devpod.Status, error) {
	if d.Docker == nil {
		return nil, nil
	}
	raw, err := d.Docker.StatusByLabel(ctx, devcontainerIDLabel)
	if err != nil {
		return nil, rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUnreachable,
			"docker ps failed: %v", err).Wrap(err)
	}
	out := make(map[string]devpod.Status, len(raw))
	for uid, state := range raw {
		out[uid] = devpod.FromDockerState(state)
	}
	return out, nil
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
				fmt.Sprintf("drift kart delete %s to clean up, then drift new %s", p.Name, p.Name))
	}

	// Single-kart info stays on the per-workspace `devpod status` path —
	// nothing to fan in for one kart, and this preserves the exact status
	// codes devpod itself reports (StatusBusy mid-up, etc.) which the
	// docker batch can't always distinguish.
	info := d.buildInfo(ctx, p.Name, cfg, ws, inDevpod, inGarage, nil)
	return info, nil
}

// buildInfo is the single place that assembles KartInfo so list and info
// stay in sync. statusByUID, when non-nil, short-circuits the per-
// workspace `devpod status` shell and resolves status from a precomputed
// docker batch keyed by workspace UID — the kart.list / server.status
// hot path. nil means "fall back to devpod status" (kart.info, tests).
func (d KartDeps) buildInfo(
	ctx context.Context,
	name string,
	cfg KartConfig,
	ws devpod.Workspace,
	inDevpod, inGarage bool,
	statusByUID map[string]devpod.Status,
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
	// Env refs attach unconditionally so `kart.list` surfaces what's
	// wired on a stale kart too — useful for debugging a restart that
	// can't re-up because a chest ref is missing.
	if env := envFromConfig(cfg.Env); env != nil {
		info.Env = env
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
		info.LastUsed = ws.LastUsed
		info.Status = d.resolveStatus(ctx, name, ws, statusByUID)
		if info.Status == devpod.StatusRunning {
			info.Container = containerFromConfig(cfg)
		}
	}
	return info
}

// resolveStatus picks the cheapest available status source. With a
// precomputed docker map we look up by UID (and fall through to "no
// container = stopped" when the workspace's UID isn't in the map);
// without one we shell `devpod status`.
func (d KartDeps) resolveStatus(
	ctx context.Context,
	name string,
	ws devpod.Workspace,
	statusByUID map[string]devpod.Status,
) devpod.Status {
	if statusByUID == nil {
		return d.statusFor(ctx, name)
	}
	if ws.UID == "" {
		// Older devpod versions or in-flight workspaces may not have a
		// UID yet; fall back to the per-workspace probe so we don't
		// silently misreport "stopped".
		return d.statusFor(ctx, name)
	}
	if st, ok := statusByUID[ws.UID]; ok {
		return st
	}
	return devpod.StatusStopped
}

// envFromConfig lifts persisted env refs into the info response. Empty
// blocks are dropped; an entirely empty TuneEnv returns nil so the top-
// level `env` key is omitted from JSON.
func envFromConfig(e model.TuneEnv) *KartInfoEnv {
	if e.IsEmpty() {
		return nil
	}
	out := &KartInfoEnv{}
	if len(e.Build) > 0 {
		out.Build = copyStringMap(e.Build)
	}
	if len(e.Workspace) > 0 {
		out.Workspace = copyStringMap(e.Workspace)
	}
	if len(e.Session) > 0 {
		out.Session = copyStringMap(e.Session)
	}
	return out
}

func copyStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
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
	root := config.KartsDir(d.GarageDir)
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
	path := config.KartConfigPath(d.GarageDir, name)
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			dir := config.KartDir(d.GarageDir, name)
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

// kartAutostartEnabled consults the YAML field first — set by kart.new on
// autostarted karts — and falls back to the on-disk sentinel file so pre-
// Autostart-field karts still report correctly. Agent 3 continues to write
// the sentinel during migration, so this fallback stays useful.
func (d KartDeps) kartAutostartEnabled(name string) bool {
	cfg, ok, err := d.readKartConfig(name)
	if err == nil && ok && cfg.Autostart {
		return true
	}
	if _, err := os.Stat(config.KartAutostartPath(d.GarageDir, name)); err == nil {
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
	mode := model.SourceMode(cfg.SourceMode)
	url := cfg.Repo
	if mode == "" {
		switch {
		case ws.Source.GitRepository != "":
			mode = model.SourceModeClone
			url = ws.Source.GitRepository
		case ws.Source.LocalFolder != "":
			mode = model.SourceModeStarter
			url = ws.Source.LocalFolder
		default:
			mode = model.SourceModeNone
		}
	}
	src := KartSource{Mode: string(mode)}
	if mode != model.SourceModeNone {
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

// resolveEnvBlock de-chests a single env block (build / workspace /
// session) against the current chest state. Rotated secrets land on the
// next call. Unresolvable refs surface as chest_entry_not_found with
// field + key in Data, mirroring the kart.new resolver.
//
// A nil OpenChest or empty input returns an empty map without touching
// any backend — callers can pass the result straight to devpod.
func (d KartDeps) resolveEnvBlock(block string, refs map[string]string) (map[string]string, error) {
	if len(refs) == 0 || d.OpenChest == nil {
		return nil, nil
	}
	backend, err := d.OpenChest()
	if err != nil {
		return nil, err
	}
	field := "env." + block
	out := make(map[string]string, len(refs))
	for k, ref := range refs {
		val, err := dechestRef(backend, field, k, ref)
		if err != nil {
			return nil, err
		}
		out[k] = val
	}
	return out, nil
}

// envKVPairs renders a resolved env map as sorted KEY=VALUE pairs — same
// deterministic ordering kart.new uses, so start/restart don't churn argv
// across runs.
func envKVPairs(m map[string]string) []string {
	if len(m) == 0 {
		return nil
	}
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+m[k])
	}
	return out
}
