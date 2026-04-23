package server

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// ChestPutParams is shared by chest.new (create-only) and chest.patch
// (update). Value rides in the JSON-RPC body so the secret never
// appears on argv.
type ChestPutParams struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

type ChestNameOnly struct {
	Name string `json:"name"`
}

// ChestGetResult wraps the value so the shape can grow (provenance, write
// timestamp) without breaking clients.
type ChestGetResult struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ChestNewHandler creates a new chest entry. Errors if one with the
// same name already exists — updates go through chest.patch.
func (d *Deps) ChestNewHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p ChestPutParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if err := name.Validate("chest", p.Name); err != nil {
		return nil, err
	}
	backend, err := d.openChest()
	if err != nil {
		return nil, err
	}
	if _, err := backend.Get(p.Name); err == nil {
		return nil, rpcerr.Conflict(rpcerr.TypeNameCollision,
			"chest %q already exists — use chest.patch to update", p.Name).With("name", p.Name)
	} else if !isChestNotFound(err) {
		return nil, wrapChestError(err)
	}
	if err := backend.Set(p.Name, []byte(p.Value)); err != nil {
		return nil, wrapChestError(err)
	}
	return ChestNameOnly{Name: p.Name}, nil
}

// ChestPatchHandler updates an existing chest entry's value. Mirrors
// the old chest.set semantics; the name change signals it's not a
// create path.
func (d *Deps) ChestPatchHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p ChestPutParams
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if err := name.Validate("chest", p.Name); err != nil {
		return nil, err
	}
	backend, err := d.openChest()
	if err != nil {
		return nil, err
	}
	if _, err := backend.Get(p.Name); err != nil {
		if isChestNotFound(err) {
			return nil, rpcerr.NotFound(rpcerr.TypeChestEntryNotFound,
				"chest %q not found — use chest.new to create", p.Name).With("name", p.Name)
		}
		return nil, wrapChestError(err)
	}
	if err := backend.Set(p.Name, []byte(p.Value)); err != nil {
		return nil, wrapChestError(err)
	}
	return ChestNameOnly{Name: p.Name}, nil
}

func (d *Deps) ChestGetHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p ChestNameOnly
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "chest.get: name is required")
	}
	backend, err := d.openChest()
	if err != nil {
		return nil, err
	}
	v, err := backend.Get(p.Name)
	if err != nil {
		return nil, wrapChestError(err)
	}
	return ChestGetResult{Name: p.Name, Value: string(v)}, nil
}

// ChestListHandler never returns values.
func (d *Deps) ChestListHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p struct{}
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	backend, err := d.openChest()
	if err != nil {
		return nil, err
	}
	names, err := backend.List()
	if err != nil {
		return nil, wrapChestError(err)
	}
	if names == nil {
		names = []string{}
	}
	return names, nil
}

func (d *Deps) ChestRemoveHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p ChestNameOnly
	if err := rpc.BindParams(params, &p); err != nil {
		return nil, err
	}
	if p.Name == "" {
		return nil, rpcerr.UserError(rpcerr.TypeInvalidFlag, "chest.remove: name is required")
	}
	backend, err := d.openChest()
	if err != nil {
		return nil, err
	}
	if err := backend.Remove(p.Name); err != nil {
		return nil, wrapChestError(err)
	}
	return ChestNameOnly{Name: p.Name}, nil
}

// wrapChestError: structured backend errors pass through; anything else
// becomes internal so the client still gets a typed envelope.
func wrapChestError(err error) error {
	if err == nil {
		return nil
	}
	var rerr *rpcerr.Error
	if errors.As(err, &rerr) {
		return err
	}
	return rpcerr.Internal("chest: %v", err).Wrap(err)
}

// isChestNotFound returns true when the backend raised the typed
// "entry not found" rpcerr. Used by chest.new / chest.patch to
// discriminate missing entries from real I/O errors without needing
// a separate .Exists method on every backend.
func isChestNotFound(err error) bool {
	var rerr *rpcerr.Error
	if !errors.As(err, &rerr) {
		return false
	}
	return rerr.Type == rpcerr.TypeChestEntryNotFound
}
