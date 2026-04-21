package server

import (
	"errors"

	"github.com/kurisu-agent/drift/internal/chest"
	"github.com/kurisu-agent/drift/internal/rpcerr"
)

// dechestRef dechests a single `chest:<name>` reference against the given
// backend. field/key are woven into the error message / Data map so every
// caller (kart env blocks, character PATs, future refs) produces a
// uniformly-shaped error. A non-chest-prefixed ref yields invalid_flag;
// a missing entry surfaces chest_entry_not_found with name/field/key in
// Data for the client to render.
//
// field is a short kind tag ("env.workspace", "character.pat_secret") so
// the rendered message reads naturally; key is the operator-facing
// identifier (map key or characters' own name).
func dechestRef(backend chest.Backend, field, key, ref string) (string, error) {
	name, ok := chest.ParseRef(ref)
	if !ok {
		return "", rpcerr.UserError(rpcerr.TypeInvalidFlag,
			"%s.%s must be a chest reference of the form %q; literal values are not accepted",
			field, key, chest.RefPrefix+"<name>").
			With("field", field).With("key", key)
	}
	val, err := backend.Get(name)
	if err != nil {
		var rpcErr *rpcerr.Error
		if errors.As(err, &rpcErr) && rpcErr.Type == rpcerr.TypeChestEntryNotFound {
			return "", rpcerr.New(rpcerr.CodeNotFound, rpcerr.TypeChestEntryNotFound,
				"%s.%s references missing chest entry %q", field, key, name).
				With("field", field).With("key", key).With("name", name)
		}
		return "", err
	}
	return string(val), nil
}
