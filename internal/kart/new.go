package kart

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/devpod"
	driftexec "github.com/kurisu-agent/drift/internal/exec"
	"github.com/kurisu-agent/drift/internal/model"
	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	yaml "gopkg.in/yaml.v3"
)

type NewDeps struct {
	GarageDir string
	Devpod    *devpod.Client
	Resolver  *Resolver
	// Starter: nil falls back to a default runner backed by internal/exec.
	Starter *Starter
	// Fetcher downloads --devcontainer URLs. nil means net/http.
	Fetcher DevcontainerFetcher
	// Now: nil means time.Now().UTC(). Tests pin this for stable fixtures.
	Now func() time.Time
	// Verbose, if non-nil, receives `[kart] <phase>` markers around each
	// expensive step (resolve / starter strip / devcontainer normalize /
	// layer-1 dotfiles / devpod up / install-dotfiles). nil keeps the
	// existing silent path.
	Verbose io.Writer
}

// New drives kart.new end-to-end. On any error after the kart dir is
// written, a `status: error` marker is stamped so a retry surfaces
// stale_kart rather than colliding on a corpse. Scratch tmpdirs are removed
// on every exit path.
func New(ctx context.Context, d NewDeps, f Flags) (*Result, error) {
	if d.Resolver == nil {
		return nil, rpcerr.Internal("kart.new: resolver not configured")
	}
	if d.Devpod == nil {
		return nil, rpcerr.Internal("kart.new: devpod client not configured")
	}
	if d.GarageDir == "" {
		return nil, rpcerr.Internal("kart.new: garage dir not configured")
	}

	if err := name.Validate("kart", f.Name); err != nil {
		return nil, err
	}

	kartDir := config.KartDir(d.GarageDir, f.Name)
	if _, err := os.Stat(kartDir); err == nil {
		// Distinguish a real collision (devpod knows the workspace too) from
		// a stale corpse (garage-only, from a crashed `drift new`).
		workspaces, lerr := d.Devpod.List(ctx)
		if lerr != nil {
			return nil, rpcerr.Internal("kart.new: devpod list: %v", lerr).Wrap(lerr)
		}
		inDevpod := false
		for _, w := range workspaces {
			if w.ID == f.Name {
				inDevpod = true
				break
			}
		}
		if inDevpod {
			return nil, rpcerr.Conflict(rpcerr.TypeNameCollision,
				"kart %q already exists", f.Name).With("name", f.Name)
		}
		return nil, rpcerr.Conflict(rpcerr.TypeStaleKart,
			"kart %q is stale (garage state without devpod workspace)", f.Name).
			With("kart", f.Name).
			With("suggestion",
				fmt.Sprintf("drift kart delete %s to clean up, then drift new %s", f.Name, f.Name))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, rpcerr.Internal("kart.new: stat %s: %v", kartDir, err).Wrap(err)
	}

	d.phase("resolving inputs")
	resolved, err := d.Resolver.Resolve(f)
	if err != nil {
		return nil, err
	}

	// All tmpfiles (starter clone, devcontainer download, layer-1 dotfiles)
	// live under one scratch so a single RemoveAll cleans up.
	scratch, err := os.MkdirTemp("", "drift-kart-"+f.Name+"-")
	if err != nil {
		return nil, rpcerr.Internal("kart.new: tmpdir: %v", err).Wrap(err)
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	if ctx.Err() != nil {
		return nil, ctxErr(ctx)
	}

	var source string
	switch resolved.SourceMode {
	case model.SourceModeStarter:
		d.phase("stripping starter %q", resolved.SourceURL)
		starterDir := filepath.Join(scratch, "starter")
		starter := d.Starter
		if starter == nil {
			starter = NewStarter()
		}
		if err := starter.Strip(ctx, resolved.SourceURL, starterDir, resolved.Character); err != nil {
			return nil, rpcerr.New(rpcerr.CodeDevpod, rpcerr.TypeDevpodUpFailed,
				"kart.new: starter: %v", err).Wrap(err)
		}
		source = starterDir
	case model.SourceModeClone:
		source = resolved.SourceURL
	case model.SourceModeNone:
		source = ""
	}

	if resolved.Devcontainer != "" {
		d.phase("normalizing devcontainer %q", resolved.Devcontainer)
	}
	if len(resolved.Mounts) > 0 {
		d.phase("splicing %d mount(s) into devcontainer overlay", len(resolved.Mounts))
	}
	if resolved.NormaliseUser {
		d.phase("pinning container user to character %q", resolved.CharacterName)
	}
	dcDir := filepath.Join(scratch, "devcontainer")
	dcPath, _, err := NormalizeDevcontainerWithOverlay(
		ctx, resolved.Devcontainer, dcDir,
		Overlay{
			Mounts:        resolved.Mounts,
			NormaliseUser: resolved.NormaliseUser,
			Character:     resolved.CharacterName,
		},
		d.Fetcher,
	)
	if err != nil {
		return nil, err
	}

	// Always run layer-1 so the generated tmpdir can be handed to devpod
	// uniformly; an absent character produces a no-op script.
	d.phase("generating layer-1 dotfiles")
	dotfilesDir := filepath.Join(scratch, "dotfiles")
	df, err := WriteLayer1Dotfiles(dotfilesDir, resolved.Character)
	if err != nil {
		return nil, rpcerr.Internal("kart.new: dotfiles: %v", err).Wrap(err)
	}

	// Write kart config BEFORE devpod up so an interrupt mid-up produces
	// stale-kart state (garage entry without a running workspace).
	if err := writeKartConfig(d.GarageDir, resolved, d.now()); err != nil {
		return nil, rpcerr.Internal("kart.new: write config: %v", err).Wrap(err)
	}
	if resolved.Autostart {
		if err := writeAutostartMarker(d.GarageDir, resolved.Name); err != nil {
			return nil, rpcerr.Internal("kart.new: autostart marker: %v", err).Wrap(err)
		}
	}

	// From here every error path runs kartErrCleanup, which tries to roll
	// back the devpod workspace + garage dir so a retry starts from a
	// clean slate. If cleanup itself fails mid-rollback (filesystem
	// busy, etc.) we stamp a `status: error` marker so the next
	// `drift new <same-name>` returns stale_kart rather than colliding
	// on a corpse we couldn't remove.
	kartErrCleanup := func(cause error) error {
		// Detach from ctx so cleanup still runs when the caller's ctx was
		// already cancelled (e.g. SIGINT triggered this error path).
		bg, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = d.Devpod.Delete(bg, resolved.Name)
		if err := os.RemoveAll(kartDir); err != nil {
			_ = writeErrorMarker(d.GarageDir, resolved.Name, cause)
		}
		return cause
	}

	// Layer-2 dotfiles pass through as --dotfiles. Layer-1 is a separate
	// install-dotfiles call below; devpod's flag only takes one --dotfiles.
	up := devpod.UpOpts{
		Name:                  resolved.Name,
		Source:                source,
		Provider:              "docker",
		IDE:                   "none",
		AdditionalFeatures:    resolved.Features,
		ExtraDevcontainerPath: dcPath,
		Dotfiles:              resolved.Dotfiles,
		WorkspaceEnv:          envKVPairs(resolved.Env.Workspace),
		// Build env rides on the dotfiles install script that devpod
		// kicks off from --dotfiles, scoped to that one process.
		DotfilesScriptEnv: envKVPairs(resolved.Env.Build),
		ConfigureSSH:      false,
	}
	d.phase("devpod up")
	if _, err := d.Devpod.Up(ctx, up); err != nil {
		re := rpcerr.New(rpcerr.CodeDevpod,
			rpcerr.TypeDevpodUpFailed, "kart.new: devpod up: %v", err).Wrap(err).
			With("kart", resolved.Name)
		if tail := driftexec.StderrTail(err); tail != "" {
			re = re.With(rpcerr.DataKeyDevpodStderr, tail)
		}
		// devpod up writes the in-container failure detail to stdout, not
		// stderr — capturing both is what makes silent `devpod_up_failed`
		// errors actually debuggable.
		if tail := driftexec.StdoutTail(err); tail != "" {
			re = re.With(rpcerr.DataKeyDevpodStdout, tail)
		}
		return nil, kartErrCleanup(re)
	}

	// KNOWN LIMITATION (skevetter/devpod v0.22): install-dotfiles runs
	// inside the agent context; a file:// URL written to the host tmpdir
	// isn't visible there, so the git-clone silently pulls an empty repo or
	// errors quietly. Command returns success but layer-1 files do not
	// land in the container. Planned follow-up: post-up `devpod ssh
	// --command` with the script piped over stdin.
	d.phase("installing layer-1 dotfiles")
	fileURL := "file://" + df.Path
	if err := d.Devpod.InstallDotfilesWithOpts(ctx, devpod.InstallDotfilesOpts{
		URL:        fileURL,
		ProcessEnv: envKVPairs(resolved.Env.Build),
	}); err != nil {
		return &Result{
			Name:      resolved.Name,
			Source:    KartSource{Mode: string(resolved.SourceMode), URL: resolved.SourceURL},
			Tune:      resolved.TuneName,
			Character: resolved.CharacterName,
			Autostart: resolved.Autostart,
			Dotfiles1: df,
			Warning:   fmt.Sprintf("layer-1 dotfiles install failed: %v", err),
			CreatedAt: d.now().Format(time.RFC3339),
		}, nil
	}

	return &Result{
		Name:      resolved.Name,
		Source:    KartSource{Mode: string(resolved.SourceMode), URL: resolved.SourceURL},
		Tune:      resolved.TuneName,
		Character: resolved.CharacterName,
		Autostart: resolved.Autostart,
		Dotfiles1: df,
		CreatedAt: d.now().Format(time.RFC3339),
	}, nil
}

// Result shape mirrors kart.info so clients can update their local cache
// from either response.
type Result struct {
	Name      string     `json:"name"`
	Source    KartSource `json:"source"`
	Tune      string     `json:"tune,omitempty"`
	Character string     `json:"character,omitempty"`
	Autostart bool       `json:"autostart"`
	CreatedAt string     `json:"created_at,omitempty"`
	// Dotfiles1 is informational — the scratch dir is RemoveAll'd on return.
	Dotfiles1 *DotfilesResult `json:"-"`
	Warning   string          `json:"warning,omitempty"`
}

// KartSource is an alias for model.KartSource — shared with
// internal/server so kart.new's Result and server's kart.info response
// carry identical JSON.
type KartSource = model.KartSource

func writeKartConfig(garageDir string, r *Resolved, now time.Time) error {
	if err := os.MkdirAll(config.KartDir(garageDir, r.Name), 0o700); err != nil {
		return err
	}
	cfg := model.KartConfig{
		Repo:       r.SourceURL,
		Tune:       r.TuneName,
		Character:  r.CharacterName,
		SourceMode: string(r.SourceMode),
		CreatedAt:  now.Format(time.RFC3339),
		// Autostart rides on the config now (cluster 25). The sentinel file
		// is still written by writeAutostartMarker for back-compat with
		// readers that predate the field — drop once everyone migrates.
		Autostart: r.Autostart,
	}
	// Persist the chest:<name> refs — never the resolved values. Lets
	// start/restart re-resolve from chest without the user re-declaring,
	// and `kart info` can render what's wired without exposing secrets.
	// TuneEnv is a value with `omitempty`; yaml.v3 omits the whole `env:`
	// key when every block is nil, so an empty EnvRefs round-trips cleanly.
	if !r.EnvRefs.IsEmpty() {
		cfg.Env = r.EnvRefs
	}
	if len(r.Mounts) > 0 {
		cfg.MountDirs = append([]model.Mount(nil), r.Mounts...)
	}
	if r.NormaliseUserRef != nil {
		v := *r.NormaliseUserRef
		cfg.NormaliseUser = &v
	}
	if !r.MigratedFrom.IsZero() {
		mf := r.MigratedFrom
		cfg.MigratedFrom = &mf
	}
	buf, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(config.KartConfigPath(garageDir, r.Name), buf, 0o644)
}

// writeAutostartMarker writes the legacy sentinel file so readers that
// predate the `autostart` field on KartConfig still flag the kart for
// boot-time start. The field on config.yaml is authoritative; this
// sentinel is belt-and-braces for one release and can be dropped once
// every on-disk config carries the field.
func writeAutostartMarker(garageDir, kartName string) error {
	return config.WriteFileAtomic(config.KartAutostartPath(garageDir, kartName), nil, 0o644)
}

// writeErrorMarker stamps the `status` file with "error" so the next
// drift-new sees the kart as stale and exits stale_kart (code:4).
func writeErrorMarker(garageDir, kartName string, cause error) error {
	msg := "error"
	if cause != nil {
		msg = "error: " + cause.Error()
	}
	return config.WriteFileAtomic(config.KartStatusPath(garageDir, kartName), []byte(msg), 0o644)
}

func (d NewDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

// phase emits a one-line `[kart] <msg>` marker to Verbose. nil = quiet.
// Callers always run before the expensive step so the user sees what's
// about to start, not what just finished.
func (d NewDeps) phase(format string, args ...any) {
	if d.Verbose == nil {
		return
	}
	fmt.Fprintf(d.Verbose, "[kart] "+format+"\n", args...)
}

// envKVPairs renders a resolved env map as sorted KEY=VALUE strings.
// Sorted so devpod argv is stable across runs (tests and log diffs).
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

func ctxErr(ctx context.Context) error {
	err := ctx.Err()
	if errors.Is(err, context.Canceled) {
		return rpcerr.UserError(rpcerr.TypeInvalidFlag, "kart.new: interrupted").Wrap(err)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return rpcerr.UserError(rpcerr.TypeInvalidFlag, "kart.new: deadline exceeded").Wrap(err)
	}
	return rpcerr.Internal("kart.new: %v", err).Wrap(err)
}
