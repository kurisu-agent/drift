//go:build integration

package integration_test

import (
	"encoding/json"
	"errors"
	"sort"
	"testing"
	"time"

	"github.com/kurisu-agent/drift/integration"
	"github.com/kurisu-agent/drift/internal/rpcerr"
	"github.com/kurisu-agent/drift/internal/wire"
)

// TestChestLifecycle covers the chest backend end-to-end: set stores a value,
// get round-trips it, list enumerates names only, remove deletes, and a
// subsequent get reports not_found. Value bytes travel in the JSON-RPC body
// so they never appear on argv — exercised here by passing a multi-line
// value the yaml backend must serialize via a block scalar.
func TestChestLifecycle(t *testing.T) {
	ctx := integration.TestCtx(t, 5*time.Minute)

	c, _ := integration.StartReadyCircuit(ctx, t, false)

	const multiline = "line-one\nline-two\nline-three"

	if _, err := c.LakituRPC(ctx, wire.MethodChestNew, map[string]string{
		"name": "alice-pat", "value": "ghp_deadbeef",
	}); err != nil {
		t.Fatalf("chest.new simple: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodChestNew, map[string]string{
		"name": "multi", "value": multiline,
	}); err != nil {
		t.Fatalf("chest.new multiline: %v", err)
	}

	// Round-trip both values.
	for name, want := range map[string]string{"alice-pat": "ghp_deadbeef", "multi": multiline} {
		getRaw, err := c.LakituRPC(ctx, wire.MethodChestGet, map[string]string{"name": name})
		if err != nil {
			t.Fatalf("chest.get %s: %v", name, err)
		}
		var got struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(getRaw, &got); err != nil {
			t.Fatalf("chest.get decode: %v", err)
		}
		if got.Value != want {
			t.Errorf("chest.get %s value = %q, want %q", name, got.Value, want)
		}
	}

	// List returns names only, sorted for stable comparison.
	listRaw, err := c.LakituRPC(ctx, wire.MethodChestList, nil)
	if err != nil {
		t.Fatalf("chest.list: %v", err)
	}
	var names []string
	if err := json.Unmarshal(listRaw, &names); err != nil {
		t.Fatalf("chest.list decode: %v", err)
	}
	sort.Strings(names)
	if len(names) != 2 || names[0] != "alice-pat" || names[1] != "multi" {
		t.Errorf("chest.list = %v, want [alice-pat multi]", names)
	}

	// Remove, then a second get is not_found.
	if _, err := c.LakituRPC(ctx, wire.MethodChestRemove, map[string]string{"name": "alice-pat"}); err != nil {
		t.Fatalf("chest.remove: %v", err)
	}
	if _, err := c.LakituRPC(ctx, wire.MethodChestGet, map[string]string{"name": "alice-pat"}); err == nil {
		t.Fatal("chest.get after remove: want error")
	} else {
		var re *rpcerr.Error
		if !errors.As(err, &re) || re.Code != rpcerr.CodeNotFound {
			t.Errorf("chest.get after remove: want not_found, got %T %v", err, err)
		}
	}
}
