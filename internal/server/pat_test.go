package server_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/internal/config"
	"github.com/kurisu-agent/drift/internal/pat"
	"github.com/kurisu-agent/drift/internal/server"
	"github.com/kurisu-agent/drift/internal/wire"
	"gopkg.in/yaml.v3"
)

func newPatTestDeps(t *testing.T) (string, *server.Deps) {
	t.Helper()
	garage := t.TempDir()
	if err := os.MkdirAll(filepath.Join(garage, "pats"), 0o755); err != nil {
		t.Fatal(err)
	}
	// LoadServer requires a real config.yaml — write the bare minimum so
	// openChest finds a YAMLFile backend.
	cfg := config.DefaultServer()
	if err := config.SaveServer(filepath.Join(garage, "config.yaml"), cfg); err != nil {
		t.Fatal(err)
	}
	return garage, &server.Deps{GarageDir: garage}
}

func TestPatNew_RoundTrip(t *testing.T) {
	garage, d := newPatTestDeps(t)

	params, _ := json.Marshal(wire.PatPutParams{
		Slug:        "work-pat",
		Token:       pat.FineGrainedPrefix + "abcdef0123456789",
		Name:        "Test PAT",
		Description: "Test description",
		Owner:       "example-user",
		ExpiresAt:   "2026-07-28",
		CreatedAt:   "2026-04-29",
		Repos:       []string{"example-user/agentic-worktrees-mcp", "example-user/agentic-sandbox"},
		Perms:       []string{"issues: read", "metadata: read", "code: write", "workflows: write"},
	})
	res, err := d.PatNewHandler(context.Background(), params)
	if err != nil {
		t.Fatalf("PatNewHandler: %v", err)
	}
	out, ok := res.(wire.PatResult)
	if !ok {
		t.Fatalf("result type: got %T", res)
	}
	if out.Slug != "work-pat" || out.Pat.ChestRef != "chest:pat-work-pat-2026-07-28" {
		t.Fatalf("result mismatch: %+v", out)
	}

	// On-disk yaml has the metadata; the chest holds the literal token.
	buf, err := os.ReadFile(filepath.Join(garage, "pats", "work-pat.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	var got pat.Pat
	if err := yaml.Unmarshal(buf, &got); err != nil {
		t.Fatal(err)
	}
	if got.Owner != "example-user" || got.ExpiresAt != "2026-07-28" {
		t.Errorf("yaml round-trip lost fields: %+v", got)
	}
	if len(got.Scopes.Repos) != 2 || got.Scopes.Repos[0] != "example-user/agentic-worktrees-mcp" {
		t.Errorf("repos: %v", got.Scopes.Repos)
	}

	chestBuf, err := os.ReadFile(filepath.Join(garage, "chest", "secrets.yaml"))
	if err != nil {
		t.Fatalf("chest read: %v", err)
	}
	if !strings.Contains(string(chestBuf), pat.FineGrainedPrefix+"abcdef0123456789") {
		t.Errorf("chest entry missing token; got:\n%s", chestBuf)
	}
}

func TestPatNew_RejectsClassicPrefix(t *testing.T) {
	_, d := newPatTestDeps(t)

	params, _ := json.Marshal(wire.PatPutParams{
		Slug:  "classic",
		Token: "ghp_some_classic_token_here",
	})
	_, err := d.PatNewHandler(context.Background(), params)
	if err == nil {
		t.Fatal("PatNewHandler accepted ghp_* token; want fine-grained-only error")
	}
	if !strings.Contains(err.Error(), "fine-grained") {
		t.Errorf("error should mention fine-grained: %v", err)
	}
}

func TestPatNew_RejectsEmptyToken(t *testing.T) {
	_, d := newPatTestDeps(t)
	params, _ := json.Marshal(wire.PatPutParams{Slug: "no-token"})
	if _, err := d.PatNewHandler(context.Background(), params); err == nil {
		t.Fatal("PatNewHandler accepted empty token; want error")
	}
}

func TestPatNew_RejectsExisting(t *testing.T) {
	_, d := newPatTestDeps(t)

	params, _ := json.Marshal(wire.PatPutParams{
		Slug:  "dup",
		Token: pat.FineGrainedPrefix + "first",
	})
	if _, err := d.PatNewHandler(context.Background(), params); err != nil {
		t.Fatalf("first PatNewHandler: %v", err)
	}
	if _, err := d.PatNewHandler(context.Background(), params); err == nil {
		t.Fatal("second PatNewHandler on same slug: want collision error, got nil")
	}
}

func TestPatUpdate_EmptyTokenLeavesChestAlone(t *testing.T) {
	garage, d := newPatTestDeps(t)

	originalToken := pat.FineGrainedPrefix + "original-secret"
	params, _ := json.Marshal(wire.PatPutParams{
		Slug:  "rotate",
		Token: originalToken,
		Owner: "old-owner",
	})
	if _, err := d.PatNewHandler(context.Background(), params); err != nil {
		t.Fatalf("PatNewHandler: %v", err)
	}

	// Update metadata only — empty token preserves the chest entry.
	updateParams, _ := json.Marshal(wire.PatPutParams{
		Slug:  "rotate",
		Token: "",
		Owner: "new-owner",
		Repos: []string{"new-owner/new-repo"},
	})
	if _, err := d.PatUpdateHandler(context.Background(), updateParams); err != nil {
		t.Fatalf("PatUpdateHandler: %v", err)
	}

	chestBuf, _ := os.ReadFile(filepath.Join(garage, "chest", "secrets.yaml"))
	if !strings.Contains(string(chestBuf), originalToken) {
		t.Errorf("chest token should be unchanged; got:\n%s", chestBuf)
	}

	yamlBuf, _ := os.ReadFile(filepath.Join(garage, "pats", "rotate.yaml"))
	var got pat.Pat
	_ = yaml.Unmarshal(yamlBuf, &got)
	if got.Owner != "new-owner" {
		t.Errorf("owner not updated: %q", got.Owner)
	}
	if len(got.Scopes.Repos) != 1 || got.Scopes.Repos[0] != "new-owner/new-repo" {
		t.Errorf("repos not updated: %v", got.Scopes.Repos)
	}
}

func TestPatUpdate_NewTokenReplacesChest(t *testing.T) {
	garage, d := newPatTestDeps(t)

	if _, err := d.PatNewHandler(context.Background(), mustJSON(t, wire.PatPutParams{
		Slug:  "rotate2",
		Token: pat.FineGrainedPrefix + "old",
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := d.PatUpdateHandler(context.Background(), mustJSON(t, wire.PatPutParams{
		Slug:  "rotate2",
		Token: pat.FineGrainedPrefix + "new",
	})); err != nil {
		t.Fatalf("PatUpdateHandler: %v", err)
	}
	chestBuf, _ := os.ReadFile(filepath.Join(garage, "chest", "secrets.yaml"))
	if strings.Contains(string(chestBuf), pat.FineGrainedPrefix+"old") {
		t.Errorf("old token should be gone; got:\n%s", chestBuf)
	}
	if !strings.Contains(string(chestBuf), pat.FineGrainedPrefix+"new") {
		t.Errorf("new token missing; got:\n%s", chestBuf)
	}
}

func TestPatUpdate_NotFoundForMissing(t *testing.T) {
	_, d := newPatTestDeps(t)
	if _, err := d.PatUpdateHandler(context.Background(), mustJSON(t, wire.PatPutParams{
		Slug:  "ghost",
		Token: pat.FineGrainedPrefix + "x",
	})); err == nil {
		t.Fatal("PatUpdateHandler on missing slug: want not-found error, got nil")
	}
}

func TestPatList_SortedAndNoToken(t *testing.T) {
	_, d := newPatTestDeps(t)
	for _, slug := range []string{"zeta", "alpha", "mid"} {
		if _, err := d.PatNewHandler(context.Background(), mustJSON(t, wire.PatPutParams{
			Slug:  slug,
			Token: pat.FineGrainedPrefix + slug + "-secret",
		})); err != nil {
			t.Fatalf("seed %q: %v", slug, err)
		}
	}
	res, err := d.PatListHandler(context.Background(), mustJSON(t, struct{}{}))
	if err != nil {
		t.Fatalf("PatListHandler: %v", err)
	}
	list, ok := res.([]wire.PatResult)
	if !ok {
		t.Fatalf("result type: got %T", res)
	}
	if len(list) != 3 {
		t.Fatalf("len = %d, want 3", len(list))
	}
	want := []string{"alpha", "mid", "zeta"}
	for i, p := range list {
		if p.Slug != want[i] {
			t.Errorf("list[%d].Slug = %q, want %q", i, p.Slug, want[i])
		}
		// The list payload must never carry the literal token.
		if strings.Contains(string(mustJSON(t, p)), pat.FineGrainedPrefix) {
			t.Errorf("list[%d] leaked token-shaped string", i)
		}
	}
}

func TestPatRemove_DropsChestAndYAML(t *testing.T) {
	garage, d := newPatTestDeps(t)

	if _, err := d.PatNewHandler(context.Background(), mustJSON(t, wire.PatPutParams{
		Slug:  "doomed",
		Token: pat.FineGrainedPrefix + "remove-me",
	})); err != nil {
		t.Fatal(err)
	}
	if _, err := d.PatRemoveHandler(context.Background(), mustJSON(t, wire.PatSlugOnly{Slug: "doomed"})); err != nil {
		t.Fatalf("PatRemoveHandler: %v", err)
	}

	if _, err := os.Stat(filepath.Join(garage, "pats", "doomed.yaml")); !os.IsNotExist(err) {
		t.Errorf("yaml still on disk: err=%v", err)
	}
	chestBuf, _ := os.ReadFile(filepath.Join(garage, "chest", "secrets.yaml"))
	if strings.Contains(string(chestBuf), "remove-me") {
		t.Errorf("chest entry survived removal:\n%s", chestBuf)
	}
}

func TestPatUpdate_RotatesChestKeyOnExpiryChange(t *testing.T) {
	garage, d := newPatTestDeps(t)
	if _, err := d.PatNewHandler(context.Background(), mustJSON(t, wire.PatPutParams{
		Slug:      "rotate-me",
		Token:     pat.FineGrainedPrefix + "v1token",
		ExpiresAt: "2026-06-01",
	})); err != nil {
		t.Fatalf("seed: %v", err)
	}
	chestPath := filepath.Join(garage, "chest", "secrets.yaml")
	before, _ := os.ReadFile(chestPath)
	if !strings.Contains(string(before), "pat-rotate-me-2026-06-01") {
		t.Fatalf("expected chest entry pat-rotate-me-2026-06-01 after pat.new; got:\n%s", before)
	}

	// Rotate: new token, new expiry.
	res, err := d.PatUpdateHandler(context.Background(), mustJSON(t, wire.PatPutParams{
		Slug:      "rotate-me",
		Token:     pat.FineGrainedPrefix + "v2token",
		ExpiresAt: "2026-09-01",
	}))
	if err != nil {
		t.Fatalf("PatUpdateHandler: %v", err)
	}
	got := res.(wire.PatResult)
	if got.Pat.ChestRef != "chest:pat-rotate-me-2026-09-01" {
		t.Fatalf("chest_ref: got %q, want chest:pat-rotate-me-2026-09-01", got.Pat.ChestRef)
	}
	after, _ := os.ReadFile(chestPath)
	if strings.Contains(string(after), "pat-rotate-me-2026-06-01") {
		t.Errorf("old chest entry pat-rotate-me-2026-06-01 should be gone; got:\n%s", after)
	}
	if !strings.Contains(string(after), "pat-rotate-me-2026-09-01") {
		t.Errorf("new chest entry pat-rotate-me-2026-09-01 missing; got:\n%s", after)
	}
	if !strings.Contains(string(after), "v2token") {
		t.Errorf("new token v2token missing from chest; got:\n%s", after)
	}
	if strings.Contains(string(after), "v1token") {
		t.Errorf("old token v1token should have been removed; got:\n%s", after)
	}
}

func TestPatShow_HappyPath(t *testing.T) {
	_, d := newPatTestDeps(t)
	if _, err := d.PatNewHandler(context.Background(), mustJSON(t, wire.PatPutParams{
		Slug:      "show-me",
		Token:     pat.FineGrainedPrefix + "abcdef",
		Name:      "Show me",
		Owner:     "example-user",
		ExpiresAt: "2026-09-01",
		CreatedAt: "2026-04-29",
		Repos:     []string{"example-user/repo"},
		Perms:     []string{"contents: read"},
	})); err != nil {
		t.Fatalf("seed: %v", err)
	}
	res, err := d.PatShowHandler(context.Background(), mustJSON(t, wire.PatSlugOnly{Slug: "show-me"}))
	if err != nil {
		t.Fatalf("PatShowHandler: %v", err)
	}
	got, ok := res.(wire.PatResult)
	if !ok {
		t.Fatalf("result type: %T", res)
	}
	if got.Slug != "show-me" || got.Pat.Owner != "example-user" || got.Pat.ExpiresAt != "2026-09-01" {
		t.Fatalf("PatShow result mismatch: %+v", got)
	}
	if got.Pat.ChestRef != "chest:pat-show-me-2026-09-01" {
		t.Errorf("chest_ref: got %q want chest:pat-show-me-2026-09-01", got.Pat.ChestRef)
	}
}

func TestPatShow_NotFoundForMissing(t *testing.T) {
	_, d := newPatTestDeps(t)
	_, err := d.PatShowHandler(context.Background(), mustJSON(t, wire.PatSlugOnly{Slug: "ghost"}))
	if err == nil {
		t.Fatal("expected pat_not_found, got nil")
	}
}

func TestPatShow_RejectsEmptySlug(t *testing.T) {
	_, d := newPatTestDeps(t)
	_, err := d.PatShowHandler(context.Background(), mustJSON(t, wire.PatSlugOnly{Slug: ""}))
	if err == nil {
		t.Fatal("expected invalid_flag, got nil")
	}
}

func TestPatRemove_RefusesWhenKartReferences(t *testing.T) {
	garage, d := newPatTestDeps(t)

	if _, err := d.PatNewHandler(context.Background(), mustJSON(t, wire.PatPutParams{
		Slug:  "in-use",
		Token: pat.FineGrainedPrefix + "abcdef",
	})); err != nil {
		t.Fatal(err)
	}

	// Stand up two karts that reference the slug, plus one that doesn't.
	mustWriteKart := func(name, slug string) {
		t.Helper()
		dir := filepath.Join(garage, "karts", name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		buf := []byte("repo: example.com/x\n")
		if slug != "" {
			buf = append(buf, []byte("pat_slug: "+slug+"\n")...)
		}
		if err := os.WriteFile(filepath.Join(dir, "config.yaml"), buf, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mustWriteKart("alpha", "in-use")
	mustWriteKart("bravo", "in-use")
	mustWriteKart("charlie", "")

	_, err := d.PatRemoveHandler(context.Background(), mustJSON(t, wire.PatSlugOnly{Slug: "in-use"}))
	if err == nil {
		t.Fatal("PatRemoveHandler must refuse when karts reference the slug")
	}
	if !strings.Contains(err.Error(), "alpha") || !strings.Contains(err.Error(), "bravo") {
		t.Errorf("error should list dependent karts; got %v", err)
	}
	if strings.Contains(err.Error(), "charlie") {
		t.Errorf("error should not list non-referencing karts; got %v", err)
	}
	if _, statErr := os.Stat(filepath.Join(garage, "pats", "in-use.yaml")); statErr != nil {
		t.Errorf("yaml should still exist after refused remove: %v", statErr)
	}
}

func TestPatRemove_AllowsWhenNoKartReferences(t *testing.T) {
	garage, d := newPatTestDeps(t)

	if _, err := d.PatNewHandler(context.Background(), mustJSON(t, wire.PatPutParams{
		Slug:  "free",
		Token: pat.FineGrainedPrefix + "freebie",
	})); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(garage, "karts", "noref")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.yaml"), []byte("repo: x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := d.PatRemoveHandler(context.Background(), mustJSON(t, wire.PatSlugOnly{Slug: "free"})); err != nil {
		t.Fatalf("PatRemoveHandler should succeed when no kart references slug: %v", err)
	}
}

func TestPatRemove_NotFound(t *testing.T) {
	_, d := newPatTestDeps(t)
	if _, err := d.PatRemoveHandler(context.Background(), mustJSON(t, wire.PatSlugOnly{Slug: "ghost"})); err == nil {
		t.Fatal("PatRemoveHandler on missing slug: want not-found, got nil")
	}
}

func TestPatFindForClone_AllShapesAndOrdering(t *testing.T) {
	garage, d := newPatTestDeps(t)
	// Three pats, all valid candidates for "acme/widget":
	//   literal  — names acme/widget directly, expires 2026-08-01
	//   wildcard — owner-wildcard "acme/*", expires 2026-09-01 (longer-lived)
	//   blanket  — repos_all + owner=acme, no expiry (sorts last)
	// One off-target pat that must NOT match.
	mustWrite := func(slug string, p pat.Pat) {
		t.Helper()
		buf, err := yaml.Marshal(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(garage, "pats", slug+".yaml"), buf, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	mustWrite("literal", pat.Pat{
		Slug: "literal", ChestRef: "chest:literal",
		Owner: "acme", ExpiresAt: "2026-08-01", CreatedAt: "2026-04-01",
		Scopes: pat.Scopes{Repos: []string{"acme/widget", "acme/other"}},
	})
	mustWrite("wildcard", pat.Pat{
		Slug: "wildcard", ChestRef: "chest:wildcard",
		Owner: "acme", ExpiresAt: "2026-09-01", CreatedAt: "2026-04-15",
		Scopes: pat.Scopes{Repos: []string{"acme/*"}},
	})
	mustWrite("blanket", pat.Pat{
		Slug: "blanket", ChestRef: "chest:blanket",
		Owner: "acme", CreatedAt: "2026-04-20",
		Scopes: pat.Scopes{ReposAll: true},
	})
	mustWrite("offtarget", pat.Pat{
		Slug: "offtarget", ChestRef: "chest:offtarget",
		Owner: "globex", ExpiresAt: "2027-01-01",
		Scopes: pat.Scopes{Repos: []string{"globex/inator"}},
	})

	res, err := d.PatFindForCloneHandler(context.Background(),
		mustJSON(t, wire.PatFindForCloneParams{Owner: "acme", Repo: "widget"}))
	if err != nil {
		t.Fatalf("PatFindForCloneHandler: %v", err)
	}
	got, ok := res.([]wire.PatResult)
	if !ok {
		t.Fatalf("result type: %T", res)
	}
	gotSlugs := make([]string, len(got))
	for i, r := range got {
		gotSlugs[i] = r.Slug
	}
	// wildcard (longest expiry) → literal → blanket (no expiry sorts last).
	wantSlugs := []string{"wildcard", "literal", "blanket"}
	if !equalStringSlices(gotSlugs, wantSlugs) {
		t.Fatalf("matches: got %v, want %v", gotSlugs, wantSlugs)
	}
}

func TestPatFindForClone_NoMatchesEmpty(t *testing.T) {
	_, d := newPatTestDeps(t)
	res, err := d.PatFindForCloneHandler(context.Background(),
		mustJSON(t, wire.PatFindForCloneParams{Owner: "acme", Repo: "widget"}))
	if err != nil {
		t.Fatalf("PatFindForCloneHandler: %v", err)
	}
	got, ok := res.([]wire.PatResult)
	if !ok {
		t.Fatalf("result type: %T", res)
	}
	if len(got) != 0 {
		t.Fatalf("expected empty slice, got %v", got)
	}
}

func TestPatFindForClone_RejectsEmptyOwnerOrRepo(t *testing.T) {
	_, d := newPatTestDeps(t)
	if _, err := d.PatFindForCloneHandler(context.Background(),
		mustJSON(t, wire.PatFindForCloneParams{Owner: "", Repo: "widget"})); err == nil {
		t.Fatal("expected error for empty owner")
	}
	if _, err := d.PatFindForCloneHandler(context.Background(),
		mustJSON(t, wire.PatFindForCloneParams{Owner: "acme", Repo: ""})); err == nil {
		t.Fatal("expected error for empty repo")
	}
}

func TestPatDaysRemainingBoundaries(t *testing.T) {
	now := mustTime(t, "2026-04-29")
	cases := []struct {
		expires string
		want    *int
	}{
		{"", nil},
		{"not-a-date", nil},
		{"2026-04-29", intp(0)},  // today
		{"2026-04-30", intp(1)},  // tomorrow
		{"2026-04-28", intp(-1)}, // yesterday
		{"2026-05-13", intp(14)}, // 14d threshold (still warns)
		{"2026-05-14", intp(15)}, // just past threshold
	}
	for _, tc := range cases {
		got := server.PatDaysRemainingForTest(&pat.Pat{ExpiresAt: tc.expires}, now)
		switch {
		case got == nil && tc.want == nil:
		case got == nil || tc.want == nil:
			t.Errorf("expires=%q: got=%v, want=%v", tc.expires, got, tc.want)
		case *got != *tc.want:
			t.Errorf("expires=%q: got=%d, want=%d", tc.expires, *got, *tc.want)
		}
	}
}

func intp(i int) *int { return &i }

func mustTime(t *testing.T, s string) time.Time {
	t.Helper()
	v, err := time.Parse("2006-01-02", s)
	if err != nil {
		t.Fatalf("parse %q: %v", s, err)
	}
	return v
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func mustJSON(t *testing.T, v any) json.RawMessage {
	t.Helper()
	buf, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return buf
}
