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

	"github.com/kurisu-agent/drift/internal/chest"
	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/pat"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
	"gopkg.in/yaml.v3"
)

// PatChestKeyPrefix namespaces PAT-backing chest entries so they don't
// collide with hand-managed `chest:<name>` references the user may set
// independently (env vars, dotfiles_repo URLs). PATs land at
// chest:pat-<slug>-<expires_at>; the expiry suffix means rotating a
// token creates a new chest key and the old entry can be removed
// independently rather than overwriting in-place.
const PatChestKeyPrefix = "pat-"

// patChestKey returns the chest entry name a given slug's token should
// land under. The expires_at suffix is omitted when empty (PATs without
// a recorded expiry register at chest:pat-<slug>).
func patChestKey(slug, expiresAt string) string {
	expiresAt = strings.TrimSpace(expiresAt)
	if expiresAt == "" {
		return PatChestKeyPrefix + slug
	}
	return PatChestKeyPrefix + slug + "-" + expiresAt
}

// PatNewHandler creates a new pat. Slug is the on-disk handle; the chest
// entry name is `pat-<slug>` so PAT-backing tokens are namespaced from
// other chest references. Token is required and must be a fine-grained
// PAT; classic PATs (`ghp_*`) are a v1 non-goal and rejected here so the
// failure surfaces at registration rather than at clone time.
func (d *Deps) PatNewHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p wire.PatPutParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if err := name.Validate("pat", p.Slug); err != nil {
		return nil, err
	}
	if p.Token == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "pat.new: token is required")
	}
	if !strings.HasPrefix(p.Token, pat.FineGrainedPrefix) {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"pat.new: only fine-grained PATs (%q) are supported", pat.FineGrainedPrefix+"…").
			With("slug", p.Slug)
	}

	path := d.patPath(p.Slug)
	if _, err := os.Stat(path); err == nil {
		return nil, rpcerr.Conflict(rpcerr.TypeNameCollision,
			"pat %q already exists — use pat.update to edit", p.Slug).With("slug", p.Slug)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return nil, rpcerr.Internal("pat.new: stat %s: %v", path, err).Wrap(err)
	}

	backend, err := d.openChest()
	if err != nil {
		return nil, err
	}
	chestKey := patChestKey(p.Slug, p.ExpiresAt)
	if err := backend.Set(chestKey, []byte(p.Token)); err != nil {
		return nil, wrapChestError(err)
	}

	rec := pat.Pat{
		Slug:        p.Slug,
		ChestRef:    chest.RefPrefix + chestKey,
		Name:        p.Name,
		Description: p.Description,
		Owner:       p.Owner,
		ExpiresAt:   p.ExpiresAt,
		CreatedAt:   p.CreatedAt,
		Scopes: pat.Scopes{
			Repos:     p.Repos,
			ReposAll:  p.ReposAll,
			Perms:     p.Perms,
			UserPerms: p.UserPerms,
		},
	}
	if err := writePat(path, &rec); err != nil {
		return nil, err
	}
	return wire.PatResult{Slug: p.Slug, Pat: rec}, nil
}

// PatUpdateHandler refreshes an existing pat. Empty Token leaves the
// chest entry untouched — that's the "scopes/expiry rotated but the
// secret didn't change" path. Non-empty Token follows the same prefix
// rules as pat.new.
func (d *Deps) PatUpdateHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p wire.PatPutParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if err := name.Validate("pat", p.Slug); err != nil {
		return nil, err
	}

	existing, err := d.loadPat(p.Slug)
	if err != nil {
		return nil, err
	}

	newChestKey := patChestKey(p.Slug, p.ExpiresAt)
	chestRef := chest.RefPrefix + newChestKey

	if p.Token != "" {
		if !strings.HasPrefix(p.Token, pat.FineGrainedPrefix) {
			return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag,
				"pat.update: only fine-grained PATs (%q) are supported", pat.FineGrainedPrefix+"…").
				With("slug", p.Slug)
		}
		backend, err := d.openChest()
		if err != nil {
			return nil, err
		}
		if err := backend.Set(newChestKey, []byte(p.Token)); err != nil {
			return nil, wrapChestError(err)
		}
		// If the old chest_ref points at a different chest entry (legacy
		// un-prefixed key, or a rotation that changed expires_at) drop
		// the orphaned token now that the new entry is durable.
		if oldKey, ok := strings.CutPrefix(existing.ChestRef, chest.RefPrefix); ok && oldKey != "" && oldKey != newChestKey {
			if err := backend.Remove(oldKey); err != nil && !isChestNotFound(err) {
				return nil, wrapChestError(err)
			}
		}
	} else if existing.ChestRef != chestRef {
		// No new token, but the metadata change (e.g. expires_at) means
		// the chest entry should live under a new key. Migrate the
		// existing token to the new key so chest_ref stays authoritative.
		oldKey, ok := strings.CutPrefix(existing.ChestRef, chest.RefPrefix)
		if ok && oldKey != "" {
			backend, err := d.openChest()
			if err != nil {
				return nil, err
			}
			val, err := backend.Get(oldKey)
			if err != nil {
				return nil, wrapChestError(err)
			}
			if err := backend.Set(newChestKey, val); err != nil {
				return nil, wrapChestError(err)
			}
			if oldKey != newChestKey {
				if err := backend.Remove(oldKey); err != nil && !isChestNotFound(err) {
					return nil, wrapChestError(err)
				}
			}
		}
	}

	rec := pat.Pat{
		Slug:        p.Slug,
		ChestRef:    chestRef,
		Name:        p.Name,
		Description: p.Description,
		Owner:       p.Owner,
		ExpiresAt:   p.ExpiresAt,
		CreatedAt:   p.CreatedAt,
		Scopes: pat.Scopes{
			Repos:     p.Repos,
			ReposAll:  p.ReposAll,
			Perms:     p.Perms,
			UserPerms: p.UserPerms,
		},
	}
	if err := writePat(d.patPath(p.Slug), &rec); err != nil {
		return nil, err
	}
	return wire.PatResult{Slug: p.Slug, Pat: rec}, nil
}

// PatListHandler returns every registered pat sorted by slug. The
// literal token never appears here — the chest reference does, mirroring
// character.list / chest.list which surface secret pointers but never
// secret values.
func (d *Deps) PatListHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	names, err := listYAMLNames(d.patDir())
	if err != nil {
		return nil, rpcerr.Internal("pat.list: %v", err).Wrap(err)
	}
	out := make([]wire.PatResult, 0, len(names))
	for _, n := range names {
		rec, err := d.loadPat(n)
		if err != nil {
			return nil, err
		}
		out = append(out, wire.PatResult{Slug: n, Pat: *rec})
	}
	return out, nil
}

// PatShowHandler returns the full metadata record for one slug. Mirrors
// tune.show / character.show. The chest entry's literal token is never
// surfaced — only the chest reference, like the other registry views.
func (d *Deps) PatShowHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p wire.PatSlugOnly
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Slug == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "pat.show: slug is required")
	}
	rec, err := d.loadPat(p.Slug)
	if err != nil {
		return nil, err
	}
	return wire.PatResult{Slug: p.Slug, Pat: *rec}, nil
}

// PatRemoveHandler drops both the yaml record and the chest entry. A
// missing chest entry is tolerated — the yaml is the source of truth
// for "is this pat known", and a half-deleted record from a prior failed
// remove shouldn't block a clean retry.
//
// Refuses with `pat_in_use` when one or more karts reference the slug
// via `pat_slug:` on their kart YAML — losing the slug would orphan the
// kart's PAT injection on the next dotfiles regen. The error lists the
// dependent kart names so the user can clear them by hand (a future
// `drift kart pat clear <kart>` flow makes this one-shot).
func (d *Deps) PatRemoveHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p wire.PatSlugOnly
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Slug == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "pat.remove: slug is required")
	}

	rec, err := d.loadPat(p.Slug)
	if err != nil {
		return nil, err
	}

	users, err := d.kartsReferencingPATSlug(p.Slug)
	if err != nil {
		return nil, err
	}
	if len(users) > 0 {
		return nil, rpcerr.UserError(rpcerr.TypePatInUse,
			"pat %q is referenced by %d kart(s): %s — clear the reference on those karts before deleting",
			p.Slug, len(users), strings.Join(users, ", ")).
			With("slug", p.Slug).With("karts", users)
	}

	chestName, ok := strings.CutPrefix(rec.ChestRef, chest.RefPrefix)
	if ok && chestName != "" {
		backend, err := d.openChest()
		if err != nil {
			return nil, err
		}
		if err := backend.Remove(chestName); err != nil && !isChestNotFound(err) {
			return nil, wrapChestError(err)
		}
	}
	if err := os.Remove(d.patPath(p.Slug)); err != nil {
		return nil, rpcerr.Internal("pat.remove: %v", err).Wrap(err)
	}
	return wire.PatSlugOnly{Slug: p.Slug}, nil
}

// kartsReferencingPATSlug returns the names of every garage kart whose
// config.yaml has `pat_slug: <slug>`. Tolerates a missing karts/ dir
// (returns nil), and silently skips kart dirs without a config.yaml or
// with a malformed one — they're already in a stale state that
// kart.list flags separately, and refusing to delete a PAT because of
// one of those would be a worse user experience than letting the delete
// through.
func (d *Deps) kartsReferencingPATSlug(slug string) ([]string, error) {
	garage, err := d.garageDir()
	if err != nil {
		return nil, err
	}
	root := config.KartsDir(garage)
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, rpcerr.Internal("pat.remove: read %s: %v", root, err).Wrap(err)
	}
	var users []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		buf, err := os.ReadFile(config.KartConfigPath(garage, e.Name()))
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				continue
			}
			return nil, rpcerr.Internal("pat.remove: read kart config: %v", err).Wrap(err)
		}
		var cfg struct {
			PATSlug string `yaml:"pat_slug"`
		}
		if err := yaml.Unmarshal(buf, &cfg); err != nil {
			// Malformed kart yaml is not our problem at delete time.
			continue
		}
		if cfg.PATSlug == slug {
			users = append(users, e.Name())
		}
	}
	sort.Strings(users)
	return users, nil
}

// PatFindForCloneHandler returns every registered PAT whose scope covers
// `<owner>/<repo>`. Match shapes:
//
//   - literal `<owner>/<repo>` in scopes.repos
//   - owner-wildcard `<owner>/*` in scopes.repos
//   - scopes.repos_all is true and pat.owner is `<owner>`
//
// Results are sorted by longest time-to-expiry first (PATs with no
// expires_at sort last so they don't bury the time-bounded matches the
// user actually has to rotate), then by created_at descending. The CLI
// renders the picker; this handler is purely a query.
func (d *Deps) PatFindForCloneHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p wire.PatFindForCloneParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	owner := strings.TrimSpace(p.Owner)
	repo := strings.TrimSpace(p.Repo)
	if owner == "" || repo == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"pat.find_for_clone: owner and repo are both required")
	}
	full := owner + "/" + repo

	names, err := listYAMLNames(d.patDir())
	if err != nil {
		return nil, rpcerr.Internal("pat.find_for_clone: %v", err).Wrap(err)
	}
	matches := make([]wire.PatResult, 0, len(names))
	for _, n := range names {
		rec, err := d.loadPat(n)
		if err != nil {
			return nil, err
		}
		if patCoversRepo(rec, owner, full) {
			matches = append(matches, wire.PatResult{Slug: n, Pat: *rec})
		}
	}
	sortPATsByExpiryDesc(matches)
	return matches, nil
}

// patCoversRepo encapsulates the match shapes documented on
// PatFindForCloneHandler. Owner is compared case-insensitively (GitHub
// logins are case-insensitive); repo names are case-insensitive too.
func patCoversRepo(rec *pat.Pat, owner, full string) bool {
	ownerLow := strings.ToLower(owner)
	fullLow := strings.ToLower(full)
	if rec.Scopes.ReposAll && strings.EqualFold(rec.Owner, owner) {
		return true
	}
	wildcard := ownerLow + "/*"
	for _, r := range rec.Scopes.Repos {
		rl := strings.ToLower(strings.TrimSpace(r))
		if rl == fullLow || rl == wildcard {
			return true
		}
	}
	return false
}

// sortPATsByExpiryDesc orders matches with the longest-lived first.
// "" expires_at sorts last (treated as "no expiry recorded" — the user
// is going to rotate something with a date sooner than something
// without). Within equal expiry buckets, the newer pat (by CreatedAt)
// wins so a freshly rotated token surfaces ahead of a stale one with
// the same end-date.
func sortPATsByExpiryDesc(matches []wire.PatResult) {
	sort.SliceStable(matches, func(i, j int) bool {
		ei, ej := matches[i].Pat.ExpiresAt, matches[j].Pat.ExpiresAt
		switch {
		case ei == "" && ej == "":
			// fall through to created_at tiebreak
		case ei == "":
			return false
		case ej == "":
			return true
		case ei != ej:
			return ei > ej
		}
		return matches[i].Pat.CreatedAt > matches[j].Pat.CreatedAt
	})
}

func (d *Deps) patDir() string {
	g, _ := d.garageDir()
	return filepath.Join(g, "pats")
}

func (d *Deps) patPath(slug string) string {
	return filepath.Join(d.patDir(), slug+".yaml")
}

func (d *Deps) loadPat(slug string) (*pat.Pat, error) {
	path := d.patPath(slug)
	buf, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, rpcerr.NotFound(rpcerr.TypePatNotFound,
				"pat %q not found — use pat.new to create", slug).With("slug", slug)
		}
		return nil, rpcerr.Internal("pat: %v", err).Wrap(err)
	}
	var rec pat.Pat
	if err := yaml.Unmarshal(buf, &rec); err != nil {
		return nil, rpcerr.Internal("pat: decode %s: %v", path, err).Wrap(err)
	}
	return &rec, nil
}

func writePat(path string, rec *pat.Pat) error {
	buf, err := yaml.Marshal(rec)
	if err != nil {
		return rpcerr.Internal("pat: marshal: %v", err).Wrap(err)
	}
	if err := config.WriteFileAtomic(path, buf, 0o644); err != nil {
		return rpcerr.Internal("pat: %v", err).Wrap(err)
	}
	return nil
}
