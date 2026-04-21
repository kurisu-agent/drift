//go:build integration

package integration_test

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// TestCharacterLifecycle drives character.add/list/show/remove via lakitu rpc
// on a fresh circuit. The drift CLI does not expose `character` subcommands
// directly (the warmup wizard is the production path), so this is the most
// faithful end-to-end coverage we have for the garage-file flow.
func TestCharacterLifecycle(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)

	// Add — the happy path with a chest-prefixed pat_secret so the pat_secret
	// validation branch is exercised (literal tokens are rejected server-side).
	addParams := map[string]any{
		"name":        "alice",
		"git_name":    "Alice Example",
		"git_email":   "alice@example.com",
		"github_user": "alice",
		"pat_secret":  "chest:alice-pat",
	}
	if _, err := c.LakituRPC(ctx, wire.MethodCharacterAdd, addParams); err != nil {
		t.Fatalf("character.add: %v", err)
	}

	// List — must contain alice.
	listRaw, err := c.LakituRPC(ctx, wire.MethodCharacterList, nil)
	if err != nil {
		t.Fatalf("character.list: %v", err)
	}
	var list []struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(listRaw, &list); err != nil {
		t.Fatalf("character.list: decode: %v\nraw=%s", err, listRaw)
	}
	if len(list) != 1 || list[0].Name != "alice" {
		t.Fatalf("character.list = %v, want [alice]", list)
	}

	// Show — pat_secret surfaces verbatim (it's a chest reference, not a secret).
	showRaw, err := c.LakituRPC(ctx, wire.MethodCharacterShow, map[string]string{"name": "alice"})
	if err != nil {
		t.Fatalf("character.show: %v", err)
	}
	var show struct {
		Name      string `json:"name"`
		GitEmail  string `json:"git_email"`
		PATSecret string `json:"pat_secret"`
	}
	if err := json.Unmarshal(showRaw, &show); err != nil {
		t.Fatalf("character.show: decode: %v\nraw=%s", err, showRaw)
	}
	if show.Name != "alice" || show.GitEmail != "alice@example.com" {
		t.Errorf("character.show = %+v, want alice/alice@example.com", show)
	}
	if show.PATSecret != "chest:alice-pat" {
		t.Errorf("character.show pat_secret = %q, want chest:alice-pat", show.PATSecret)
	}

	// Duplicate add is a name_collision conflict.
	if _, err := c.LakituRPC(ctx, wire.MethodCharacterAdd, addParams); err == nil {
		t.Fatal("character.add duplicate: want error, got nil")
	} else {
		var re *rpcerr.Error
		if !errors.As(err, &re) || re.Code != rpcerr.CodeConflict {
			t.Errorf("duplicate add: want conflict, got %T %v", err, err)
		}
	}

	// Remove — second call is a not_found.
	if _, err := c.LakituRPC(ctx, wire.MethodCharacterRemove, map[string]string{"name": "alice"}); err != nil {
		t.Fatalf("character.remove: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodCharacterRemove, map[string]string{"name": "alice"}); err == nil {
		t.Fatal("character.remove of missing: want error")
	} else {
		var re *rpcerr.Error
		if !errors.As(err, &re) || re.Code != rpcerr.CodeNotFound {
			t.Errorf("remove missing: want not_found, got %T %v", err, err)
		}
	}
}
