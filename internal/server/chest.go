package server

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/kurisu-agent/drift/internal/name"
	"github.com/kurisu-agent/drift/internal/rpc"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// ChestSetParams.Value rides in the JSON-RPC body so the secret never
// appears on argv.
type ChestSetParams struct {
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

// ChestSetHandler: chest keys share the kart-name shape so character
// yaml's `chest:<name>` references validate offline.
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
