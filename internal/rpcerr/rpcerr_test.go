package rpcerr_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

func TestMarshalJSON_includesTypeAndData(t *testing.T) {
	e := rpcerr.NotFound(rpcerr.TypeKartNotFound, "kart %q not found", "myproject").
		With("kart", "myproject").
		With("circuit", "my-server")
	buf, err := json.Marshal(e)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(buf, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	want := map[string]any{
		"code":    float64(3),
		"message": `kart "myproject" not found`,
		"data": map[string]any{
			"type":    "kart_not_found",
			"kart":    "myproject",
			"circuit": "my-server",
		},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("marshal mismatch (-want +got):\n%s", diff)
	}
}

func TestIs_matchesOnType(t *testing.T) {
	sentinel := rpcerr.NotFound(rpcerr.TypeKartNotFound, "template")
	actual := rpcerr.NotFound(rpcerr.TypeKartNotFound, "specific kart").With("kart", "x")
	if !errors.Is(actual, sentinel) {
		t.Fatal("errors.Is should match on type")
	}
	other := rpcerr.NotFound(rpcerr.TypeCharacterNotFound, "char")
	if errors.Is(actual, other) {
		t.Fatal("errors.Is should not match different types")
	}
}

func TestUnwrap_exposesCause(t *testing.T) {
	cause := errors.New("disk full")
	e := rpcerr.Internal("write failed").Wrap(cause)
	if !errors.Is(e, cause) {
		t.Fatal("errors.Is should walk to Cause")
	}
}
