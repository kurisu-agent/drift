//go:build integration

package integration_test

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// TestTuneEnvWorkspaceInjection verifies the env.workspace block on a tune
// lands as --set-env flags on devpod up and persists into the kart config
// so kart.start re-applies them with the latest chest value on restart.
// Covers Stage #2 / #3 from plans/06 (workspace lifetime container env).
func TestTuneEnvWorkspaceInjection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c, rec := setupTuneCircuit(ctx, t)

	const (
		chestName  = "workspace-openai"
		chestValue = "sk-workspace-abc123"
		envKey     = "OPENAI_API_KEY"
	)
	if _, err := c.LakituRPC(ctx, wire.MethodChestSet, map[string]string{
		"name": chestName, "value": chestValue,
	}); err != nil {
		t.Fatalf("chest.set: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodTuneSet, map[string]any{
		"name": "envtune-ws",
		"env": map[string]any{
			"workspace": map[string]string{
				envKey: "chest:" + chestName,
			},
		},
	}); err != nil {
		t.Fatalf("tune.set: %v", err)
	}

	kart := c.KartName("env-ws")
	if _, stderr, code := c.Drift(ctx, "new", kart, "--tune", "envtune-ws"); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	up := rec.FindUp(ctx)
	if up == nil {
		t.Fatalf("no devpod up invocation recorded")
	}
	wantKV := envKey + "=" + chestValue
	if !argvHas(up.Argv, "--set-env", wantKV) {
		t.Errorf("up argv missing --set-env %q, got %v", wantKV, up.Argv)
	}

	// kart.list surfaces the chest reference (never the value) under
	// env.workspace. The devpod recorder shim returns an empty `list`
	// so the kart renders as stale in the garage — which is fine for
	// this check, we just need the persisted env refs to round-trip.
	listRaw, err := c.LakituRPC(ctx, wire.MethodKartList, nil)
	if err != nil {
		t.Fatalf("kart.list: %v", err)
	}
	var listRes struct {
		Karts []struct {
			Name string `json:"name"`
			Env  *struct {
				Workspace map[string]string `json:"workspace"`
			} `json:"env"`
		} `json:"karts"`
	}
	if err := json.Unmarshal(listRaw, &listRes); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	var gotEnvRef string
	for _, k := range listRes.Karts {
		if k.Name == kart && k.Env != nil {
			gotEnvRef = k.Env.Workspace[envKey]
		}
	}
	if gotEnvRef != "chest:"+chestName {
		t.Errorf("kart.list env.workspace[%s] = %q, want chest:%s", envKey, gotEnvRef, chestName)
	}
	if strings.Contains(string(listRaw), chestValue) {
		t.Errorf("kart.list leaked chest value: %s", listRaw)
	}

	// Restart should re-read chest and re-apply --set-env with the current
	// value. Rotate the chest entry and assert the restart's devpod up
	// picks up the new value.
	const rotatedValue = "sk-workspace-rotated"
	if _, err := c.LakituRPC(ctx, wire.MethodChestSet, map[string]string{
		"name": chestName, "value": rotatedValue,
	}); err != nil {
		t.Fatalf("chest.set rotate: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodKartRestart, map[string]string{"name": kart}); err != nil {
		t.Fatalf("kart.restart: %v", err)
	}
	ups := findAllUps(rec.Invocations(ctx))
	if len(ups) < 2 {
		t.Fatalf("want >=2 devpod up invocations, got %d", len(ups))
	}
	restartUp := ups[len(ups)-1]
	wantRotated := envKey + "=" + rotatedValue
	if !argvHas(restartUp.Argv, "--set-env", wantRotated) {
		t.Errorf("restart up argv missing rotated --set-env %q, got %v", wantRotated, restartUp.Argv)
	}
}

// TestTuneEnvBuildInjection verifies env.build entries land in the process
// env of the install-dotfiles invocation only — never in the workspace
// env. Covers Stage #1 (one-shot build-time secret).
func TestTuneEnvBuildInjection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c, rec := setupTuneCircuit(ctx, t)

	const (
		chestName  = "build-token"
		chestValue = "ghp_buildtoken_xyz"
		envKey     = "GITHUB_TOKEN"
	)
	if _, err := c.LakituRPC(ctx, wire.MethodChestSet, map[string]string{
		"name": chestName, "value": chestValue,
	}); err != nil {
		t.Fatalf("chest.set: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodTuneSet, map[string]any{
		"name": "envtune-build",
		"env": map[string]any{
			"build": map[string]string{
				envKey: "chest:" + chestName,
			},
		},
	}); err != nil {
		t.Fatalf("tune.set: %v", err)
	}

	kart := c.KartName("env-build")
	if _, stderr, code := c.Drift(ctx, "new", kart, "--tune", "envtune-build"); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	inv := rec.FindInstallDotfiles(ctx)
	if inv == nil {
		t.Fatalf("no install-dotfiles invocation recorded")
	}
	want := envKey + "=" + chestValue
	if !envHas(inv.Env, want) {
		t.Errorf("install-dotfiles env missing %q; got %d vars (not printed to avoid secret leak)", want, len(inv.Env))
	}
	// Same secret must NOT ride on the workspace env — build is one-shot.
	up := rec.FindUp(ctx)
	if up != nil && argvHasValuePrefix(up.Argv, "--set-env", envKey+"=") {
		t.Errorf("build secret leaked into devpod up --set-env: argv=%v", up.Argv)
	}
}

// TestTuneEnvSessionInjection verifies kart.session_env returns resolved
// KEY=VALUE pairs and swallows chest rotation. The connect path appends
// these to the remote devpod ssh invocation as --set-env flags; this test
// exercises the RPC directly so it doesn't need a working mosh/ssh
// transport inside the harness.
func TestTuneEnvSessionInjection(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c, _ := setupTuneCircuit(ctx, t)

	const (
		chestName  = "session-anthropic"
		chestValue = "sk-ant-session-456"
		envKey     = "ANTHROPIC_API_KEY"
	)
	if _, err := c.LakituRPC(ctx, wire.MethodChestSet, map[string]string{
		"name": chestName, "value": chestValue,
	}); err != nil {
		t.Fatalf("chest.set: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodTuneSet, map[string]any{
		"name": "envtune-sess",
		"env": map[string]any{
			"session": map[string]string{
				envKey: "chest:" + chestName,
			},
		},
	}); err != nil {
		t.Fatalf("tune.set: %v", err)
	}

	kart := c.KartName("env-sess")
	if _, stderr, code := c.Drift(ctx, "new", kart, "--tune", "envtune-sess"); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	raw, err := c.LakituRPC(ctx, wire.MethodKartSessionEnv, map[string]string{"name": kart})
	if err != nil {
		t.Fatalf("kart.session_env: %v", err)
	}
	var res struct {
		Env []string `json:"env"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		t.Fatalf("decode session_env: %v", err)
	}
	want := envKey + "=" + chestValue
	found := false
	for _, kv := range res.Env {
		if kv == want {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("session_env = %v, want entry %q", res.Env, want)
	}
}

// TestTuneEnvMissingChestEntry verifies kart.new fails fast with
// chest_entry_not_found (block + key in Data) when an env ref points at
// an absent chest entry, and doesn't leave a devpod workspace behind.
func TestTuneEnvMissingChestEntry(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c, rec := setupTuneCircuit(ctx, t)

	if _, err := c.LakituRPC(ctx, wire.MethodTuneSet, map[string]any{
		"name": "envtune-missing",
		"env": map[string]any{
			"workspace": map[string]string{
				"MISSING": "chest:does-not-exist",
			},
		},
	}); err != nil {
		t.Fatalf("tune.set: %v", err)
	}

	kart := c.KartName("env-missing")
	_, stderr, code := c.Drift(ctx, "new", kart, "--tune", "envtune-missing")
	if code == 0 {
		t.Fatalf("drift new unexpectedly succeeded with missing chest ref")
	}
	if !strings.Contains(stderr, "chest_entry_not_found") &&
		!strings.Contains(stderr, "does-not-exist") {
		t.Errorf("stderr doesn't mention chest_entry_not_found or key name: %s", stderr)
	}
	if up := rec.FindUp(ctx); up != nil {
		t.Errorf("devpod up ran despite chest miss: argv=%v", up.Argv)
	}
}

// TestTuneEnvRejectsLiteralValue verifies the tune.set handler enforces
// the chest-only invariant on env entries (mirrors the character.add PAT
// literal-rejection) so a typo on a user's side can't accidentally stash
// a secret outside the chest.
func TestTuneEnvRejectsLiteralValue(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c, _ := setupTuneCircuit(ctx, t)

	_, err := c.LakituRPC(ctx, wire.MethodTuneSet, map[string]any{
		"name": "envtune-literal",
		"env": map[string]any{
			"workspace": map[string]string{
				"TOKEN": "ghp_literal_token_must_reject",
			},
		},
	})
	if err == nil {
		t.Fatalf("tune.set accepted a literal env value")
	}
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != rpcerr.TypeInvalidFlag {
		t.Errorf("want invalid_flag rpcerr, got %v", err)
	}
}

// argvHasValuePrefix reports whether the flag-value pair has a value that
// starts with prefix. Used where the value's suffix is the chest-resolved
// secret — the test asserts presence/absence of the KEY= half, not the
// literal value.
func argvHasValuePrefix(argv []string, flag, prefix string) bool {
	for i := 0; i+1 < len(argv); i++ {
		if argv[i] == flag && strings.HasPrefix(argv[i+1], prefix) {
			return true
		}
	}
	return false
}

// envHas reports whether env contains the exact KEY=VALUE pair. Linear
// scan is fine — shim-captured env is at most a few hundred entries.
func envHas(env []string, want string) bool {
	for _, kv := range env {
		if kv == want {
			return true
		}
	}
	return false
}

// findAllUps collects every `devpod up` invocation across the recorder
// log. Used to compare successive kart.new / kart.restart --set-env sets.
func findAllUps(invs []integration.DevpodInvocation) []integration.DevpodInvocation {
	var out []integration.DevpodInvocation
	for _, inv := range invs {
		if len(inv.Argv) > 0 && inv.Argv[0] == "up" {
			out = append(out, inv)
		}
	}
	return out
}
