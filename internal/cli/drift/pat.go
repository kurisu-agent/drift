package drift

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/huh"
	"github.com/kurisu-agent/drift/internal/cli/errfmt"
	"github.com/kurisu-agent/drift/internal/cli/style"
	"github.com/kurisu-agent/drift/internal/githttp"
	"github.com/kurisu-agent/drift/internal/pat"
	"github.com/kurisu-agent/drift/internal/wire"
)

// patCmd is the `drift pat …` namespace.
type patCmd struct {
	New    patNewCmd    `cmd:"" help:"Register a new GitHub fine-grained PAT (interactive)."`
	Update patUpdateCmd `cmd:"" help:"Update an existing PAT's token and/or scopes."`
	Info   patInfoCmd   `cmd:"" help:"Show one PAT's metadata (slug, owner, scopes, expires_at)."`
	Delete patDeleteCmd `cmd:"" help:"Delete a registered PAT (drops both the metadata and the chest entry)."`
}

type patNewCmd struct {
	Slug string `arg:"" optional:"" help:"Short identifier used to reference this PAT. Derived from the paste body's name when omitted."`
}

type patUpdateCmd struct {
	Slug string `arg:"" help:"Slug of the PAT to update."`
}

type patInfoCmd struct {
	Slug string `arg:"" help:"Slug of the PAT to show."`
}

type patDeleteCmd struct {
	Slug  string `arg:"" help:"Slug of the PAT to delete."`
	Force bool   `short:"y" name:"yes" aliases:"force" help:"Skip the interactive confirmation prompt."`
}

func runPatNew(ctx context.Context, io IO, root *CLI, cmd patNewCmd, deps deps) int {
	return runPatPut(ctx, io, root, deps, cmd.Slug, false)
}

func runPatUpdate(ctx context.Context, io IO, root *CLI, cmd patUpdateCmd, deps deps) int {
	return runPatPut(ctx, io, root, deps, cmd.Slug, true)
}

// runPatPut drives the interactive registration flow. The shape stays
// identical between new and update — the only divergence is whether an
// empty token prompt is allowed (update) or fatal (new).
func runPatPut(ctx context.Context, io IO, root *CLI, deps deps, slug string, update bool) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	p := style.For(io.Stdout, root.Output == "json")
	settingsURL := "https://github.com/settings/personal-access-tokens/new"
	if update {
		settingsURL = "https://github.com/settings/personal-access-tokens"
	}
	fmt.Fprintln(io.Stdout, p.Dim("→ open this URL in a browser:"))
	fmt.Fprintln(io.Stdout, "  "+settingsURL)
	fmt.Fprintln(io.Stdout)

	// gosec: G101 false-positive — these are UI titles, not credentials.
	tokenTitle := "Paste the new fine-grained PAT (github_pat_…)" //nolint:gosec
	if update {
		tokenTitle = "Paste the new PAT, or leave empty to keep the current token" //nolint:gosec
	}
	var token string
	tokenInput := huh.NewInput().
		Title(tokenTitle).
		EchoMode(huh.EchoModePassword).
		Value(&token)
	if err := huh.NewForm(huh.NewGroup(tokenInput)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return 1
		}
		return errfmt.Emit(io.Stderr, err)
	}
	token = strings.TrimSpace(token)

	if token == "" && !update {
		return errfmt.Emit(io.Stderr, errors.New("token is required for `drift pat new`"))
	}
	if token != "" {
		if !strings.HasPrefix(token, pat.FineGrainedPrefix) {
			return errfmt.Emit(io.Stderr,
				fmt.Errorf("only fine-grained PATs (%s…) are supported in v1; classic ghp_* tokens will be rejected", pat.FineGrainedPrefix))
		}
		login, err := probeGitHub(ctx, token)
		if err != nil {
			return errfmt.Emit(io.Stderr, fmt.Errorf("github token probe: %w", err))
		}
		fmt.Fprintln(io.Stdout, p.Dim(fmt.Sprintf("✓ token valid; owned by @%s", login)))
		fmt.Fprintln(io.Stdout)
	}

	pasteTitle := "Paste the PAT settings page body (Ctrl+A to select all on the page, paste here, then Tab/Enter to continue)"
	var body string
	pasteInput := huh.NewText().Title(pasteTitle).Value(&body)
	if err := huh.NewForm(huh.NewGroup(pasteInput)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return 1
		}
		return errfmt.Emit(io.Stderr, err)
	}

	parsed := pat.Parse(body, time.Now())

	if slug == "" {
		// `drift pat new` (no arg) derives the slug from the parsed name.
		// For update we already know the slug — Kong enforced the arg.
		derived, err := pat.Slugify(parsed.Name)
		if err != nil {
			return errfmt.Emit(io.Stderr,
				fmt.Errorf("can't derive a slug from paste body name %q (%w); pass an explicit slug: drift pat new <slug>", parsed.Name, err))
		}
		slug = derived
		fmt.Fprintln(io.Stdout, p.Dim(fmt.Sprintf("→ derived slug %q from paste body name %q", slug, parsed.Name)))
	}

	fmt.Fprintln(io.Stdout, formatPatPreview(slug, parsed))

	confirm := true
	confirmField := huh.NewConfirm().
		Title("Save this PAT?").
		Affirmative("Save").
		Negative("Cancel").
		Value(&confirm)
	if err := huh.NewForm(huh.NewGroup(confirmField)).Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return 1
		}
		return errfmt.Emit(io.Stderr, err)
	}
	if !confirm {
		fmt.Fprintln(io.Stderr, "aborted; no PAT written")
		return 1
	}

	method := wire.MethodPatNew
	if update {
		method = wire.MethodPatUpdate
	}
	params := wire.PatPutParams{
		Slug:        slug,
		Token:       token,
		Name:        parsed.Name,
		Description: parsed.Description,
		Owner:       parsed.Owner,
		ExpiresAt:   parsed.ExpiresAt,
		CreatedAt:   parsed.CreatedAt,
		Repos:       parsed.Repos,
		ReposAll:    parsed.ReposAll,
		Perms:       parsed.Perms,
		UserPerms:   parsed.UserPerms,
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, method, params, &raw); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}

	if root.Output == "json" {
		fmt.Fprintln(io.Stdout, string(raw))
		return 0
	}
	fmt.Fprintln(io.Stdout, p.Dim(fmt.Sprintf("✓ pat %q saved on circuit %q", slug, circuit)))
	return 0
}

// runPatDelete drops a registered PAT. Mirrors `drift kart delete`'s
// confirm-on-TTY / -y-on-stdin contract — destructive verbs that fail
// closed when stdin can't answer prompts are the safer default.
func runPatDelete(ctx context.Context, io IO, root *CLI, cmd patDeleteCmd, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if !cmd.Force {
		confirmed, err := confirmPatDelete(io, circuit, cmd.Slug)
		if err != nil {
			return errfmt.Emit(io.Stderr, err)
		}
		if !confirmed {
			fmt.Fprintln(io.Stderr, "aborted")
			return 1
		}
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodPatRemove, wire.PatSlugOnly{Slug: cmd.Slug}, &raw); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if root != nil && root.Output == "json" {
		fmt.Fprintln(io.Stdout, string(raw))
		return 0
	}
	p := style.For(io.Stdout, false)
	fmt.Fprintln(io.Stdout, p.Dim(fmt.Sprintf("✓ pat %q deleted on circuit %q", cmd.Slug, circuit)))
	return 0
}

func confirmPatDelete(io IO, circuit, slug string) (bool, error) {
	if !stdinIsTTY(io.Stdin) {
		return false, errors.New("drift pat delete requires -y on non-interactive stdin")
	}
	p := style.For(io.Stderr, false)
	fmt.Fprintf(io.Stderr, "%s delete pat %q on circuit %q? [y/N]: ",
		p.Warn("!"), slug, circuit)
	var ans string
	if _, err := fmt.Fscanln(io.Stdin, &ans); err != nil {
		return false, nil
	}
	switch strings.ToLower(strings.TrimSpace(ans)) {
	case "y", "yes":
		return true, nil
	}
	return false, nil
}

// runPatInfo shows one PAT's metadata. Mirrors `drift kart info` in
// shape — text mode prints a key/value block, json mode prints the raw
// pat.show response. Token material never appears here (the chest_ref
// is shown, the dechested value is not).
func runPatInfo(ctx context.Context, io IO, root *CLI, cmd patInfoCmd, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodPatShow, wire.PatSlugOnly{Slug: cmd.Slug}, &raw); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if root != nil && root.Output == "json" {
		fmt.Fprintln(io.Stdout, string(raw))
		return 0
	}
	var res wire.PatResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	writePatInfoBlock(io.Stdout, style.For(io.Stdout, false), res)
	return 0
}

// writePatInfoBlock renders the text-mode `drift pat info` output. Layout
// mirrors `drift kart info`: a bold accent-colored title row (the slug)
// followed by an indented dim-key/value block. Days-to-expiry annotates
// the expires_at line with the same ≤14d warn / past-zero error glyphs
// used elsewhere in the PAT surface.
func writePatInfoBlock(w io.Writer, p *style.Palette, res wire.PatResult) {
	fmt.Fprintf(w, "%s\n", p.Bold(p.Accent(res.Slug)))
	printIf := func(label, value string) {
		if value == "" {
			return
		}
		fmt.Fprintf(w, "  %s %s\n", p.Dim(label+":"), value)
	}
	printIf("name", res.Pat.Name)
	printIf("description", res.Pat.Description)
	printIf("owner", res.Pat.Owner)
	printIf("chest_ref", res.Pat.ChestRef)
	printIf("created_at", res.Pat.CreatedAt)
	expires := res.Pat.ExpiresAt
	if expires != "" {
		if t, err := time.Parse("2006-01-02", expires); err == nil {
			d := int(time.Until(t).Hours() / 24)
			switch {
			case d < 0:
				expires = p.Warn(fmt.Sprintf("%s ✗ expired", res.Pat.ExpiresAt))
			case d <= 14:
				expires = p.Warn(fmt.Sprintf("%s ⚠ %dd", res.Pat.ExpiresAt, d))
			default:
				expires = fmt.Sprintf("%s (%dd)", res.Pat.ExpiresAt, d)
			}
		}
	}
	printIf("expires_at", expires)
	switch {
	case res.Pat.Scopes.ReposAll:
		printIf("repos", "all repositories owned by "+res.Pat.Owner)
	case len(res.Pat.Scopes.Repos) > 0:
		fmt.Fprintf(w, "  %s\n", p.Dim("repos:"))
		for _, r := range res.Pat.Scopes.Repos {
			fmt.Fprintf(w, "    - %s\n", r)
		}
	}
	if len(res.Pat.Scopes.Perms) > 0 {
		fmt.Fprintf(w, "  %s\n", p.Dim("perms:"))
		for _, perm := range res.Pat.Scopes.Perms {
			fmt.Fprintf(w, "    - %s\n", perm)
		}
	}
	if len(res.Pat.Scopes.UserPerms) > 0 {
		fmt.Fprintf(w, "  %s\n", p.Dim("user_perms:"))
		for _, perm := range res.Pat.Scopes.UserPerms {
			fmt.Fprintf(w, "    - %s\n", perm)
		}
	}
}

// runPats is the plural list verb (`drift pats`). One row per registered
// pat, sorted by slug. Days-to-expiry highlights pats that are within
// rotation reach so the user spots them at a glance.
func runPats(ctx context.Context, io IO, root *CLI, deps deps) int {
	_, circuit, err := resolveCircuit(root, deps)
	if err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	var raw json.RawMessage
	if err := deps.call(ctx, circuit, wire.MethodPatList, struct{}{}, &raw); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if root != nil && root.Output == "json" {
		fmt.Fprintln(io.Stdout, string(raw))
		return 0
	}
	var list []wire.PatResult
	if err := json.Unmarshal(raw, &list); err != nil {
		return errfmt.Emit(io.Stderr, err)
	}
	if len(list) == 0 {
		fmt.Fprintf(io.Stdout, "no pats registered on circuit %q (try `drift pat new`)\n", circuit)
		return 0
	}
	writePatTable(io.Stdout, style.For(io.Stdout, false), list)
	return 0
}

// writePatTable renders the human-facing table. Days-to-expiry is the
// rotation signal — anything ≤14 days flags warn.
func writePatTable(w io.Writer, p *style.Palette, list []wire.PatResult) {
	rows := make([][]string, 0, len(list))
	urgent := make([]bool, 0, len(list))
	now := time.Now()
	for _, e := range list {
		expires := dashIfEmpty(e.Pat.ExpiresAt)
		days := ""
		warn := false
		if t, err := time.Parse("2006-01-02", e.Pat.ExpiresAt); err == nil {
			d := int(t.Sub(now).Hours() / 24)
			days = fmt.Sprintf("%dd", d)
			if d <= 14 {
				warn = true
			}
		}
		repos := strconv.Itoa(len(e.Pat.Scopes.Repos))
		if e.Pat.Scopes.ReposAll {
			repos = "all"
		}
		owner := dashIfEmpty(e.Pat.Owner)
		rows = append(rows, []string{e.Slug, dashIfEmpty(e.Pat.Name), owner, repos, expires, days})
		urgent = append(urgent, warn)
	}
	writeTable(w, p, []string{"SLUG", "NAME", "OWNER", "REPOS", "EXPIRES", "IN"}, rows,
		colorCellStyler(func(row, col int) tableCell {
			switch col {
			case 0:
				return tableCell{Color: tableCellAccent}
			case 5:
				if row >= 0 && row < len(urgent) && urgent[row] {
					return tableCell{Color: tableCellWarn}
				}
			}
			return tableCell{}
		}))
}

func dashIfEmpty(s string) string {
	if s == "" {
		return "-"
	}
	return s
}

// formatPatPreview renders the parsed paste so the user sees what's
// about to be persisted before confirming. Empty fields are still
// shown so missing data is loud, not silent.
func formatPatPreview(slug string, parsed pat.ParsedPaste) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Captured from paste:\n")
	fmt.Fprintf(&b, "  slug         %s\n", slug)
	fmt.Fprintf(&b, "  name         %s\n", orDash(parsed.Name))
	fmt.Fprintf(&b, "  description  %s\n", orDash(parsed.Description))
	fmt.Fprintf(&b, "  owner        %s\n", orDash(parsed.Owner))
	fmt.Fprintf(&b, "  created_at   %s\n", orDash(parsed.CreatedAt))
	fmt.Fprintf(&b, "  expires_at   %s\n", orDash(parsed.ExpiresAt))
	switch {
	case parsed.ReposAll:
		fmt.Fprintf(&b, "  repos        all repositories owned by @%s\n", orDash(parsed.Owner))
	case len(parsed.Repos) == 0:
		fmt.Fprintf(&b, "  repos        —\n")
	default:
		fmt.Fprintf(&b, "  repos\n")
		for _, r := range parsed.Repos {
			fmt.Fprintf(&b, "    - %s\n", r)
		}
	}
	if len(parsed.Perms) == 0 {
		fmt.Fprintf(&b, "  perms        —\n")
	} else {
		fmt.Fprintf(&b, "  perms\n")
		for _, perm := range parsed.Perms {
			fmt.Fprintf(&b, "    - %s\n", perm)
		}
	}
	if len(parsed.UserPerms) > 0 {
		fmt.Fprintf(&b, "  user_perms\n")
		for _, perm := range parsed.UserPerms {
			fmt.Fprintf(&b, "    - %s\n", perm)
		}
	}
	return b.String()
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// probeGitHub hits `GET /user` with the token and returns the login on
// 200 OK. Any non-200 (401 in particular) becomes an error so the user
// finds out the token is bad before sending anything to lakitu. The
// short timeout avoids hanging on an offline workstation; users on
// flaky networks will see the error and can retry.
func probeGitHub(ctx context.Context, token string) (string, error) {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(cctx, http.MethodGet, "https://api.github.com/user", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")

	resp, err := githttp.DefaultClient().Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusUnauthorized {
		return "", errors.New("github rejected the token (401) — check that you copied it whole and that the PAT has not been revoked")
	}
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("github /user returned %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	var info struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", fmt.Errorf("decode github response: %w", err)
	}
	if info.Login == "" {
		return "", errors.New("github /user returned no login")
	}
	return info.Login, nil
}
