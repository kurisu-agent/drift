package server

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// ChestSetParams is the RPC param shape for `chest.set`. value rides inside
// the JSON-RPC body so the secret never appears on argv (plans/COMMANDS.md
// § drift chest).
type ChestSetParams struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ChestNameOnly is the param shape for chest.get / chest.remove.
type ChestNameOnly struct {
	Name string `json:"name"`
}

// ChestGetResult wraps the secret value so the shape is stable when we
// eventually add provenance (backend, write timestamp) per plans/PLAN.md.
type ChestGetResult struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ChestSetHandler stores a secret. Name validation reuses the shared
// identifier regex — chest keys share the kart-name shape so a character
// yaml's `chest:<name>` reference can be validated offline.
func (d *Deps) ChestSetHandler(_ context.Context, params json.RawMessage) (any, error) {
	var p ChestSetParams
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
	if err := backend.Set(p.Name, []byte(p.Value)); err != nil {
		return nil, wrapChestError(err)
	}
	return ChestNameOnly{Name: p.Name}, nil
}

// ChestGetHandler returns a stored secret.
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

// ChestListHandler returns the set of stored names. Values are never
// returned (plans/PLAN.md § Method catalog).
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

// ChestRemoveHandler deletes a secret.
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

// wrapChestError passes structured backend errors through and wraps anything
// else as an internal error so the client still gets a typed envelope.
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
