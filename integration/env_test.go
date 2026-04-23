//go:build integration

package integration_test

import (
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
// lands as --workspace-env flags on devpod up and persists into the kart
// config so kart.start re-applies them with the latest chest value on
// restart. Covers Stage #2 / #3 from plans/06 (workspace lifetime container
// env).
func TestTuneEnvWorkspaceInjection(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, rec := integration.StartReadyCircuit(ctx, t, true)

	const (
		chestName  = "workspace-openai"
		chestValue = "sk-workspace-abc123"
		envKey     = "OPENAI_API_KEY"
	)
	if _, err := c.LakituRPC(ctx, wire.MethodChestNew, map[string]string{
		"name": chestName, "value": chestValue,
	}); err != nil {
		t.Fatalf("chest.new: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]any{
		"name": "envtune-ws",
		"env": map[string]any{
			"workspace": map[string]string{
				envKey: "chest:" + chestName,
			},
		},
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
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
	if !integration.ArgvHas(up.Argv, "--workspace-env", wantKV) {
		t.Errorf("up argv missing --workspace-env %q, got %v", wantKV, up.Argv)
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

	// Restart should re-read chest and re-apply --workspace-env with the
	// current value. Rotate the chest entry and assert the restart's devpod
	// up picks up the new value.
	const rotatedValue = "sk-workspace-rotated"
	if _, err := c.LakituRPC(ctx, wire.MethodChestPatch, map[string]string{
		"name": chestName, "value": rotatedValue,
	}); err != nil {
		t.Fatalf("chest.patch rotate: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodKartRestart, map[string]string{"name": kart}); err != nil {
		t.Fatalf("kart.restart: %v", err)
	}
	ups := rec.FindAllUps(ctx)
	if len(ups) < 2 {
		t.Fatalf("want >=2 devpod up invocations, got %d", len(ups))
	}
	restartUp := ups[len(ups)-1]
	wantRotated := envKey + "=" + rotatedValue
	if !integration.ArgvHas(restartUp.Argv, "--workspace-env", wantRotated) {
		t.Errorf("restart up argv missing rotated --workspace-env %q, got %v", wantRotated, restartUp.Argv)
	}
}

// TestKartRecreateRebuildsWithRotatedEnv verifies `drift kart recreate`
// invokes `devpod up --recreate` (the flag is what makes a changed
// devcontainer.json actually rebuild the container) and re-reads chest
// so the rotated workspace env lands on the new container. This is the
// integration-level partner to the server unit tests in
// internal/server/kart_lifecycle_test.go — here we drive the full
// drift→lakitu→shim recorder path to prove the wire constant, handler
// registration, and CLI dispatch are all consistent.
func TestKartRecreateRebuildsWithRotatedEnv(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, rec := integration.StartReadyCircuit(ctx, t, true)

	const (
		chestName  = "recreate-openai"
		chestValue = "sk-recreate-original"
		envKey     = "OPENAI_API_KEY"
	)
	if _, err := c.LakituRPC(ctx, wire.MethodChestNew, map[string]string{
		"name": chestName, "value": chestValue,
	}); err != nil {
		t.Fatalf("chest.new: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]any{
		"name": "envtune-recreate",
		"env": map[string]any{
			"workspace": map[string]string{
				envKey: "chest:" + chestName,
			},
		},
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
	}

	kart := c.KartName("recreate")
	if _, stderr, code := c.Drift(ctx, "new", kart, "--tune", "envtune-recreate"); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	// Rotate the chest entry; recreate should pick up the new value.
	const rotatedValue = "sk-recreate-rotated"
	if _, err := c.LakituRPC(ctx, wire.MethodChestPatch, map[string]string{
		"name": chestName, "value": rotatedValue,
	}); err != nil {
		t.Fatalf("chest.patch rotate: %v", err)
	}

	// Drive the client CLI (not just the RPC) so the wiring through
	// drift.go dispatch + kartCmd + runKartLifecycle gets exercised.
	if _, stderr, code := c.Drift(ctx, "kart", "recreate", kart); code != 0 {
		t.Fatalf("drift kart recreate: code=%d stderr=%q", code, stderr)
	}

	ups := rec.FindAllUps(ctx)
	if len(ups) < 2 {
		t.Fatalf("want >=2 devpod up invocations (new + recreate), got %d", len(ups))
	}
	recreateUp := ups[len(ups)-1]

	sawRecreateFlag := false
	for _, a := range recreateUp.Argv {
		if a == "--recreate" {
			sawRecreateFlag = true
			break
		}
	}
	if !sawRecreateFlag {
		t.Errorf("recreate up argv missing --recreate flag, got %v", recreateUp.Argv)
	}

	wantRotated := envKey + "=" + rotatedValue
	if !integration.ArgvHas(recreateUp.Argv, "--workspace-env", wantRotated) {
		t.Errorf("recreate up argv missing rotated --workspace-env %q, got %v", wantRotated, recreateUp.Argv)
	}
	// Ensure the initial `new` up did NOT carry --recreate (it should
	// only be applied on the recreate verb).
	for _, a := range ups[0].Argv {
		if a == "--recreate" {
			t.Errorf("initial `drift new` up carried --recreate; argv=%v", ups[0].Argv)
		}
	}
}

// TestKartRecreateLeavesKartConfigUnchanged is the integration-level
// mirror of the same-named server unit test: confirms `drift kart
// recreate` does NOT mutate the kart's config.yaml on disk (no tune
// re-snapshot). That's the contract that differentiates recreate from
// rebuild — users who've hand-edited config.yaml rely on this.
func TestKartRecreateLeavesKartConfigUnchanged(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, true)

	// A kart with a tune set is the interesting case — rebuild would
	// re-snapshot env+mount_dirs from the tune; recreate must not.
	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]any{
		"name": "envtune-noop",
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
	}
	kart := c.KartName("recreate-noop")
	if _, stderr, code := c.Drift(ctx, "new", kart, "--tune", "envtune-noop"); code != 0 {
		t.Fatalf("drift new: code=%d stderr=%q", code, stderr)
	}

	cfgPath := "/home/" + c.User + "/.drift/garage/karts/" + kart + "/config.yaml"
	before := c.ExecInContainer(ctx, "sh", "-c", "cat "+cfgPath+" && stat -c %Y "+cfgPath)

	if _, stderr, code := c.Drift(ctx, "kart", "recreate", kart); code != 0 {
		t.Fatalf("drift kart recreate: code=%d stderr=%q", code, stderr)
	}

	after := c.ExecInContainer(ctx, "sh", "-c", "cat "+cfgPath+" && stat -c %Y "+cfgPath)
	if string(before) != string(after) {
		t.Errorf("kart config changed by recreate:\nbefore=%q\nafter=%q", before, after)
	}
}

// TestTuneEnvBuildInjection verifies env.build entries reach both
// dotfiles install paths — drift's layer-1 file:// install (process env)
// and devpod's --dotfiles install via --dotfiles-script-env on devpod up
// — without leaking into the workspace's containerEnv. Covers Stage #1
// (one-shot build-time secret).
func TestTuneEnvBuildInjection(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, rec := integration.StartReadyCircuit(ctx, t, true)

	const (
		chestName  = "build-token"
		chestValue = "ghp_buildtoken_xyz"
		envKey     = "GITHUB_TOKEN"
	)
	if _, err := c.LakituRPC(ctx, wire.MethodChestNew, map[string]string{
		"name": chestName, "value": chestValue,
	}); err != nil {
		t.Fatalf("chest.new: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]any{
		"name": "envtune-build",
		"env": map[string]any{
			"build": map[string]string{
				envKey: "chest:" + chestName,
			},
		},
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
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
	if !integration.EnvHas(inv.Env, want) {
		t.Errorf("install-dotfiles env missing %q; got %d vars (not printed to avoid secret leak)", want, len(inv.Env))
	}
	up := rec.FindUp(ctx)
	if up == nil {
		t.Fatalf("no devpod up invocation recorded")
	}
	// devpod up should carry --dotfiles-script-env so the in-container
	// dotfiles install script sees the build secret.
	if !integration.ArgvHas(up.Argv, "--dotfiles-script-env", want) {
		t.Errorf("up argv missing --dotfiles-script-env %q, got %v", want, up.Argv)
	}
	// Same secret must NOT ride on the workspace env — build is one-shot.
	if integration.ArgvHasValuePrefix(up.Argv, "--workspace-env", envKey+"=") {
		t.Errorf("build secret leaked into devpod up --workspace-env: argv=%v", up.Argv)
	}
}

// TestTuneEnvSessionInjection verifies kart.session_env returns resolved
// KEY=VALUE pairs and swallows chest rotation. The connect path appends
// these to the remote devpod ssh invocation as --set-env flags; this test
// exercises the RPC directly so it doesn't need a working mosh/ssh
// transport inside the harness.
func TestTuneEnvSessionInjection(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, true)

	const (
		chestName  = "session-anthropic"
		chestValue = "sk-ant-session-456"
		envKey     = "ANTHROPIC_API_KEY"
	)
	if _, err := c.LakituRPC(ctx, wire.MethodChestNew, map[string]string{
		"name": chestName, "value": chestValue,
	}); err != nil {
		t.Fatalf("chest.new: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]any{
		"name": "envtune-sess",
		"env": map[string]any{
			"session": map[string]string{
				envKey: "chest:" + chestName,
			},
		},
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
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
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, rec := integration.StartReadyCircuit(ctx, t, true)

	if _, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]any{
		"name": "envtune-missing",
		"env": map[string]any{
			"workspace": map[string]string{
				"MISSING": "chest:does-not-exist",
			},
		},
	}); err != nil {
		t.Fatalf("tune.new: %v", err)
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
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, true)

	_, err := c.LakituRPC(ctx, wire.MethodTuneNew, map[string]any{
		"name": "envtune-literal",
		"env": map[string]any{
			"workspace": map[string]string{
				"TOKEN": "ghp_literal_token_must_reject",
			},
		},
	})
	if err == nil {
		t.Fatalf("tune.new accepted a literal env value")
	}
	var re *rpcerr.Error
	if !errors.As(err, &re) || re.Type != rpcerr.TypeInvalidFlag {
		t.Errorf("want invalid_flag rpcerr, got %v", err)
	}
}
