package kart

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/devpod"
	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	yaml "gopkg.in/yaml.v3"
)

// NewDeps bundles the collaborators required by [New]. The handler layer
// constructs one per call; tests inject fakes for every external touchpoint.
type NewDeps struct {
	// GarageDir is the absolute path to ~/.drift/garage. The kart-side
	// config.yaml lands under GarageDir/karts/<name>/.
	GarageDir string
	// Devpod runs `devpod up` once all sources + dotfiles are ready.
	Devpod *devpod.Client
	// Resolver composes tune + character + flag defaults. Required.
	Resolver *Resolver
	// Starter performs the history-strip flow. When nil, a default runner
	// backed by internal/exec is used.
	Starter *Starter
	// Fetcher downloads --devcontainer URLs. nil means net/http.
	Fetcher DevcontainerFetcher
	// Now returns the timestamp stamped into the kart's config.yaml. nil
	// means time.Now().UTC(). Tests pin this for stable test fixtures.
	Now func() time.Time
}

// New drives kart.new end-to-end. It:
//
//  1. validates the kart name
//  2. rejects a name that already has a garage/karts/<name>/ entry
//  3. resolves flags (server defaults → tune → explicit → additive features)
//  4. materializes the starter tmpdir (if any)
//  5. normalizes --devcontainer into a file path
//  6. writes the layer-1 dotfiles tmpdir
//  7. writes garage/karts/<name>/config.yaml (and an autostart marker)
//  8. invokes devpod up with the composed arguments
//  9. cleans up the starter and dotfiles tmpdirs on success
//
// On any error after step 7, the kart dir is marked `status: error` so a
// retry sees a stale kart and surfaces the right hint (plans/PLAN.md
// § Interrupts). All tmpdirs are removed on every error path.
//
// The function is context-aware: a cancelled ctx triggers devpod cancel via
// internal/exec (SIGTERM → SIGKILL after 5s), then the deferred cleanup runs.
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

	kartDir := filepath.Join(d.GarageDir, "karts", f.Name)
	if _, err := os.Stat(kartDir); err == nil {
		// Garage dir exists. Distinguish a real collision (devpod knows the
		// workspace too) from a stale corpse (garage-only, from a crashed
		// `drift new`). Stale corpses get a suggestion the user can paste —
		// plans/PLAN.md § Stale karts / § Interrupts.
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
				fmt.Sprintf("drift delete %s to clean up, then drift new %s", f.Name, f.Name))
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, rpcerr.Internal("kart.new: stat %s: %v", kartDir, err).Wrap(err)
	}

	resolved, err := d.Resolver.Resolve(f)
	if err != nil {
		return nil, err
	}

	// Per-invocation scratch dir. Every temp file — starter clone,
	// devcontainer download, layer-1 dotfiles — lives under here so one
	// os.RemoveAll cleans the lot.
	scratch, err := os.MkdirTemp("", "drift-kart-"+f.Name+"-")
	if err != nil {
		return nil, rpcerr.Internal("kart.new: tmpdir: %v", err).Wrap(err)
	}
	// Clean scratch on any exit path. The caller has no reason to inspect
	// it after New returns.
	defer func() { _ = os.RemoveAll(scratch) }()

	// Context cancellation is observed deliberately — not just via the
	// subprocess runners — so step transitions after devpod fail quickly.
	if ctx.Err() != nil {
		return nil, ctxErr(ctx)
	}

	// Starter: clone + history-strip into a dedicated dir inside scratch.
	// For clone mode we hand devpod the git URL directly; no local dir.
	var source string
	switch resolved.SourceMode {
	case "starter":
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
	case "clone":
		source = resolved.SourceURL
	case "none":
		source = ""
	}

	// Devcontainer normalization: path passes through, JSON/URL writes to
	// scratch/devcontainer/.
	dcDir := filepath.Join(scratch, "devcontainer")
	dcPath, _, err := NormalizeDevcontainer(ctx, resolved.Devcontainer, dcDir, d.Fetcher)
	if err != nil {
		return nil, err
	}

	// Layer 1 dotfiles — always run so the generated tmpdir path can be
	// handed to devpod uniformly; when no character is attached the script
	// is a no-op.
	dotfilesDir := filepath.Join(scratch, "dotfiles")
	df, err := WriteLayer1Dotfiles(dotfilesDir, resolved.Character)
	if err != nil {
		return nil, rpcerr.Internal("kart.new: dotfiles: %v", err).Wrap(err)
	}

	// Write garage/karts/<name>/config.yaml BEFORE devpod up so an
	// interrupt mid-up produces the stale-kart state plans/PLAN.md asks
	// for — the garage entry exists without a running workspace.
	if err := writeKartConfig(kartDir, resolved, d.now()); err != nil {
		return nil, rpcerr.Internal("kart.new: write config: %v", err).Wrap(err)
	}
	if resolved.Autostart {
		if err := writeAutostartMarker(kartDir); err != nil {
			return nil, rpcerr.Internal("kart.new: autostart marker: %v", err).Wrap(err)
		}
	}

	// From here on any error should leave an `error` marker so the next
	// `drift new <same-name>` returns stale_kart rather than colliding.
	kartErrMarker := func(cause error) error {
		_ = writeErrorMarker(kartDir, cause)
		return cause
	}

	// Compose devpod up args. Layer-2 dotfiles go in as `--dotfiles <url>`;
	// layer-1 dotfiles are handled post-up with `install-dotfiles` keyed on
	// the local file:// URL — devpod's own flag only takes one --dotfiles.
	up := devpod.UpOpts{
		Name:                  resolved.Name,
		Source:                source,
		Provider:              "docker",
		IDE:                   "none",
		AdditionalFeatures:    resolved.Features,
		ExtraDevcontainerPath: dcPath,
		Dotfiles:              resolved.Dotfiles,
		ConfigureSSH:          false,
	}
	if _, err := d.Devpod.Up(ctx, up); err != nil {
		return nil, kartErrMarker(rpcerr.New(rpcerr.CodeDevpod,
			rpcerr.TypeDevpodUpFailed, "kart.new: devpod up: %v", err).Wrap(err))
	}

	// Layer-1 dotfiles: push the generated tmpdir to devpod's
	// install-dotfiles helper. file:// URL is expected by that API.
	//
	// KNOWN LIMITATION (skevetter/devpod v0.17): install-dotfiles runs
	// inside the agent context; a file:// URL written to the host tmpdir
	// isn't visible there, so git-clone silently pulls an empty repo or
	// errors quietly. The command returns success but layer-1 files do
	// not land in the container. Moving the install to a post-up `devpod
	// ssh --command` with the script piped over stdin is the planned
	// follow-up — tracked in plans/TODO.md (not yet filed).
	fileURL := "file://" + df.Path
	if err := d.Devpod.InstallDotfiles(ctx, fileURL); err != nil {
		// Non-fatal at this phase — the kart itself is up. Surface the
		// failure as a warning via the result struct; future phases can
		// escalate if users want stricter behavior.
		return &Result{
			Name:      resolved.Name,
			Source:    KartSource{Mode: resolved.SourceMode, URL: resolved.SourceURL},
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
		Source:    KartSource{Mode: resolved.SourceMode, URL: resolved.SourceURL},
		Tune:      resolved.TuneName,
		Character: resolved.CharacterName,
		Autostart: resolved.Autostart,
		Dotfiles1: df,
		CreatedAt: d.now().Format(time.RFC3339),
	}, nil
}

// Result is the success payload of kart.new. The JSON shape mirrors the
// fields kart.info emits so clients can update their local cache from
// either response.
type Result struct {
	Name      string     `json:"name"`
	Source    KartSource `json:"source"`
	Tune      string     `json:"tune,omitempty"`
	Character string     `json:"character,omitempty"`
	Autostart bool       `json:"autostart"`
	CreatedAt string     `json:"created_at,omitempty"`
	// Dotfiles1 is the layer-1 dotfiles tmpdir — left behind so a caller
	// that wants to inspect the generated tree for testing can read it.
	// In production the scratch dir is removed on New's defer, so this
	// field is informational only.
	Dotfiles1 *DotfilesResult `json:"-"`
	Warning   string          `json:"warning,omitempty"`
}

// KartSource mirrors server.KartSource — duplicated here because this
// package must stay under server in the dep graph. plans/PLAN.md § lakitu
// info kart — JSON schema.
type KartSource struct {
	Mode string `json:"mode"`
	URL  string `json:"url,omitempty"`
}

// writeKartConfig renders garage/karts/<name>/config.yaml. The shape mirrors
// the KartConfig consumed by server.kart_list / kart_info.
func writeKartConfig(kartDir string, r *Resolved, now time.Time) error {
	if err := os.MkdirAll(kartDir, 0o700); err != nil {
		return err
	}
	type onDisk struct {
		Repo       string `yaml:"repo,omitempty"`
		Tune       string `yaml:"tune,omitempty"`
		Character  string `yaml:"character,omitempty"`
		SourceMode string `yaml:"source_mode,omitempty"`
		CreatedAt  string `yaml:"created_at,omitempty"`
	}
	cfg := onDisk{
		Repo:       r.SourceURL,
		Tune:       r.TuneName,
		Character:  r.CharacterName,
		SourceMode: r.SourceMode,
		CreatedAt:  now.Format(time.RFC3339),
	}
	buf, err := yaml.Marshal(&cfg)
	if err != nil {
		return err
	}
	return config.WriteFileAtomic(filepath.Join(kartDir, "config.yaml"), buf, 0o644)
}

// writeAutostartMarker touches garage/karts/<name>/autostart. Presence is
// the signal; contents are ignored (plans/PLAN.md § Server state layout).
func writeAutostartMarker(kartDir string) error {
	return config.WriteFileAtomic(filepath.Join(kartDir, "autostart"), nil, 0o644)
}

// writeErrorMarker stamps garage/karts/<name>/status with the literal
// "error" marker so a subsequent drift new sees the kart as stale and
// bails out with stale_kart (code:4). plans/PLAN.md § Interrupts.
func writeErrorMarker(kartDir string, cause error) error {
	msg := "error"
	if cause != nil {
		msg = "error: " + cause.Error()
	}
	return config.WriteFileAtomic(filepath.Join(kartDir, "status"), []byte(msg), 0o644)
}

func (d NewDeps) now() time.Time {
	if d.Now != nil {
		return d.Now()
	}
	return time.Now().UTC()
}

// ctxErr maps a cancelled/context.DeadlineExceeded to a user-facing rpcerr
// so the client sees the right exit code.
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
