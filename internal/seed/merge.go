package seed

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// MergeFormat is the structural format a merge operates on. JSON and
// YAML have a shared `any`-tree representation post-unmarshal, so the
// deep-merge logic itself is format-agnostic; only the encode/decode
// boundary differs.
type MergeFormat string

const (
	MergeFormatJSON MergeFormat = "json"
	MergeFormatYAML MergeFormat = "yaml"
)

// MergeFormatFromPath picks a merge format from the file extension.
// Returns an error for any extension we don't know how to merge — the
// only safe set is .json / .yaml / .yml. Callers surface the error at
// render time to fail fast on a misconfigured template.
func MergeFormatFromPath(path string) (MergeFormat, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".json":
		return MergeFormatJSON, nil
	case ".yaml", ".yml":
		return MergeFormatYAML, nil
	}
	return "", fmt.Errorf("on_conflict: merge requires a .json/.yaml/.yml path, got %q", path)
}

// Merge deep-merges patch into existing for the given format and
// returns the encoded result. Empty `existing` is treated as an empty
// document (so first-seed runs degrade to a plain write).
//
// Merge semantics:
//   - object/map keys union; same key recurses.
//   - arrays/sequences: patch replaces existing wholesale (not
//     concatenated — concat-by-default tends to surprise; users who
//     want concat can use `append` instead of `merge`).
//   - scalars and type mismatches: patch wins.
func Merge(format MergeFormat, existing, patch []byte) ([]byte, error) {
	switch format {
	case MergeFormatJSON:
		return mergeJSON(existing, patch)
	case MergeFormatYAML:
		return mergeYAML(existing, patch)
	}
	return nil, fmt.Errorf("unsupported merge format %q", format)
}

func mergeJSON(existing, patch []byte) ([]byte, error) {
	var ev, pv any
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := json.Unmarshal(existing, &ev); err != nil {
			return nil, fmt.Errorf("parse existing JSON: %w", err)
		}
	}
	if len(bytes.TrimSpace(patch)) > 0 {
		if err := json.Unmarshal(patch, &pv); err != nil {
			return nil, fmt.Errorf("parse seed JSON: %w", err)
		}
	}
	merged := deepMerge(ev, pv)
	out, err := json.MarshalIndent(merged, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode merged JSON: %w", err)
	}
	out = append(out, '\n')
	return out, nil
}

func mergeYAML(existing, patch []byte) ([]byte, error) {
	var ev, pv any
	if len(bytes.TrimSpace(existing)) > 0 {
		if err := yaml.Unmarshal(existing, &ev); err != nil {
			return nil, fmt.Errorf("parse existing YAML: %w", err)
		}
	}
	if len(bytes.TrimSpace(patch)) > 0 {
		if err := yaml.Unmarshal(patch, &pv); err != nil {
			return nil, fmt.Errorf("parse seed YAML: %w", err)
		}
	}
	// yaml.Unmarshal returns map[string]any (since v3 keys are strings
	// for valid YAML mappings) so deepMerge handles them directly.
	merged := deepMerge(ev, pv)
	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(merged); err != nil {
		return nil, fmt.Errorf("encode merged YAML: %w", err)
	}
	if err := enc.Close(); err != nil {
		return nil, fmt.Errorf("close YAML encoder: %w", err)
	}
	return buf.Bytes(), nil
}

// deepMerge walks two parsed any-trees and returns the merged tree.
// Non-map values: patch wins. Maps: recurse on shared keys. Sequences
// are not concatenated — patch's sequence replaces existing's, since
// concat-by-default surprises more than it helps for typical config.
func deepMerge(existing, patch any) any {
	if patch == nil {
		return existing
	}
	em, eok := existing.(map[string]any)
	pm, pok := patch.(map[string]any)
	if !eok || !pok {
		return patch
	}
	out := make(map[string]any, len(em)+len(pm))
	for k, v := range em {
		out[k] = v
	}
	for k, v := range pm {
		if cur, ok := out[k]; ok {
			out[k] = deepMerge(cur, v)
		} else {
			out[k] = v
		}
	}
	return out
}
