// Package yamlpath applies single-field patches to YAML-tagged structs
// via dotted-path addressing, mirroring `git config` shape.
//
// Callers pass a pointer to a struct (e.g. *model.Tune) and a sequence
// of ops; Apply walks yaml tags to reach the addressed field, mutates
// the in-memory value, and returns. Persisting the mutated struct to
// disk is the caller's problem.
//
// What works:
//   - Scalar struct fields (string, bool, int) addressed by their YAML
//     tag: `starter`, `git_email`.
//   - Maps at any depth with a scalar value type (string, bool, int).
//     The final path segment is the map key: `env.build.GITHUB_TOKEN`.
//     Unknown intermediate maps are auto-created on set; unset prunes
//     empty parents so re-serialising the struct doesn't emit stale
//     map shells.
//
// What doesn't:
//   - Slice indexing (`mount_dirs[0]`). Lists round-trip via the
//     `edit` flow or via a full-replace RPC — intentional, per the
//     plan.
//   - Nested struct fields past the first level. If we grow
//     struct-of-struct config types we'll revisit; today every
//     patchable type is flat (scalars) or flat-plus-one-map
//     (`env.{build,workspace,session}.<key>`).
package yamlpath

import (
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"
)

// Op is a single patch operation. Path is dotted (segments joined by ".");
// Op is "set" or "unset"; Value is ignored on unset.
type Op struct {
	Path  string
	Op    string
	Value any
}

// OpSet / OpUnset: string constants for the Op field. Callers may use
// these to avoid typos.
const (
	OpSet   = "set"
	OpUnset = "unset"
)

// Error is returned by Apply on any failure. Kind distinguishes
// user-facing problems (unknown field, bad path) from internal ones
// (type mismatch the caller could theoretically have prevented).
type Error struct {
	Kind    string // "unknown_field", "not_scalar", "list_not_supported", "type_mismatch", "bad_op"
	Path    string
	Message string
	// Suggest is populated for "unknown_field" when a near-match exists;
	// renderers append it as "did you mean …?".
	Suggest string
}

func (e *Error) Error() string {
	if e.Suggest != "" {
		return fmt.Sprintf("%s: %s (did you mean %q?)", e.Path, e.Message, e.Suggest)
	}
	return fmt.Sprintf("%s: %s", e.Path, e.Message)
}

// Apply runs ops in order against obj (which must be a pointer to a
// struct). Partial application on error is possible by design — we
// don't clone the struct first because callers validate and
// atomic-write the marshalled result, so a partial mutation never
// reaches disk.
func Apply(obj any, ops []Op) error {
	rv := reflect.ValueOf(obj)
	if rv.Kind() != reflect.Ptr || rv.IsNil() {
		return errors.New("yamlpath.Apply: obj must be a non-nil pointer")
	}
	root := rv.Elem()
	if root.Kind() != reflect.Struct {
		return errors.New("yamlpath.Apply: obj must point to a struct")
	}
	for _, op := range ops {
		if err := applyOne(root, op); err != nil {
			return err
		}
	}
	return nil
}

func applyOne(root reflect.Value, op Op) error {
	segs := strings.Split(op.Path, ".")
	if len(segs) == 0 || segs[0] == "" {
		return &Error{Kind: "bad_path", Path: op.Path, Message: "empty path"}
	}
	switch op.Op {
	case OpSet:
		return setPath(root, op.Path, segs, op.Value)
	case OpUnset:
		return unsetPath(root, op.Path, segs)
	default:
		return &Error{Kind: "bad_op", Path: op.Path, Message: fmt.Sprintf("unknown op %q (want \"set\" or \"unset\")", op.Op)}
	}
}

// setPath walks segs against cur, auto-creating intermediate maps,
// and assigns value at the leaf. For a map-leaf segment, the final
// seg is the map key.
func setPath(cur reflect.Value, fullPath string, segs []string, value any) error {
	// Walk the first segment as a struct field.
	fv, rest, err := lookupStructField(cur, fullPath, segs)
	if err != nil {
		return err
	}
	return setInto(fv, fullPath, rest, value)
}

// setInto recurses into fv with the remaining path. fv may be a
// scalar (rest must be empty), a map (rest has one+ segments, final
// is the key), or an unsupported kind (slice/struct).
func setInto(fv reflect.Value, fullPath string, rest []string, value any) error {
	switch fv.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int64:
		if len(rest) > 0 {
			return &Error{Kind: "not_scalar", Path: fullPath,
				Message: fmt.Sprintf("cannot descend into scalar field at .%s", strings.Join(rest, "."))}
		}
		return setScalar(fv, fullPath, value)

	case reflect.Map:
		// Must target a map entry: final rest seg is the key. If
		// the map has struct values, allow one more level (map->struct->scalar);
		// today all patchable maps are map[string]scalar.
		if len(rest) == 0 {
			return &Error{Kind: "not_scalar", Path: fullPath,
				Message: "cannot set a whole map — use `edit` or a specific entry path"}
		}
		if fv.IsNil() {
			fv.Set(reflect.MakeMap(fv.Type()))
		}
		key := rest[0]
		keyV := reflect.ValueOf(key)
		elemT := fv.Type().Elem()
		if elemT.Kind() == reflect.String || elemT.Kind() == reflect.Bool ||
			elemT.Kind() == reflect.Int || elemT.Kind() == reflect.Int64 {
			if len(rest) != 1 {
				return &Error{Kind: "not_scalar", Path: fullPath,
					Message: fmt.Sprintf("cannot descend into scalar map entry at .%s", strings.Join(rest[1:], "."))}
			}
			// Build a writable scalar and assign it.
			ev := reflect.New(elemT).Elem()
			if err := setScalar(ev, fullPath, value); err != nil {
				return err
			}
			fv.SetMapIndex(keyV, ev)
			return nil
		}
		if elemT.Kind() == reflect.Struct {
			// map->struct->field: allow one more level of descent.
			// Get-or-create the entry, mutate in place, write back.
			existing := fv.MapIndex(keyV)
			slot := reflect.New(elemT).Elem()
			if existing.IsValid() {
				slot.Set(existing)
			}
			if err := setInto(slot, fullPath, rest[1:], value); err != nil {
				return err
			}
			fv.SetMapIndex(keyV, slot)
			return nil
		}
		return &Error{Kind: "not_scalar", Path: fullPath,
			Message: fmt.Sprintf("map entries of type %s are not patchable — use `edit`", elemT.Kind())}

	case reflect.Struct:
		// Descend into a nested struct via yaml tag.
		if len(rest) == 0 {
			return &Error{Kind: "not_scalar", Path: fullPath,
				Message: "cannot set a whole struct — address a leaf field or use `edit`"}
		}
		child, remaining, err := lookupStructField(fv, fullPath, rest)
		if err != nil {
			return err
		}
		return setInto(child, fullPath, remaining, value)

	case reflect.Slice:
		return &Error{Kind: "list_not_supported", Path: fullPath,
			Message: "list fields are not patchable — use `edit` to modify lists"}

	case reflect.Ptr:
		if fv.IsNil() {
			fv.Set(reflect.New(fv.Type().Elem()))
		}
		return setInto(fv.Elem(), fullPath, rest, value)

	default:
		return &Error{Kind: "not_scalar", Path: fullPath,
			Message: fmt.Sprintf("unsupported field kind %s", fv.Kind())}
	}
}

// unsetPath clears the addressed value: scalars go to the zero
// value, map entries are deleted, and empty parent maps are pruned.
func unsetPath(cur reflect.Value, fullPath string, segs []string) error {
	fv, rest, err := lookupStructField(cur, fullPath, segs)
	if err != nil {
		return err
	}
	return unsetInto(fv, fullPath, rest)
}

func unsetInto(fv reflect.Value, fullPath string, rest []string) error {
	switch fv.Kind() {
	case reflect.String, reflect.Bool, reflect.Int, reflect.Int64:
		if len(rest) > 0 {
			return &Error{Kind: "not_scalar", Path: fullPath,
				Message: fmt.Sprintf("cannot descend into scalar field at .%s", strings.Join(rest, "."))}
		}
		fv.Set(reflect.Zero(fv.Type()))
		return nil

	case reflect.Map:
		if len(rest) == 0 {
			// Nil out the whole map.
			fv.Set(reflect.Zero(fv.Type()))
			return nil
		}
		if fv.IsNil() {
			return nil // already absent
		}
		key := rest[0]
		keyV := reflect.ValueOf(key)
		elemT := fv.Type().Elem()
		if elemT.Kind() == reflect.String || elemT.Kind() == reflect.Bool ||
			elemT.Kind() == reflect.Int || elemT.Kind() == reflect.Int64 {
			if len(rest) != 1 {
				return &Error{Kind: "not_scalar", Path: fullPath,
					Message: fmt.Sprintf("cannot descend into scalar map entry at .%s", strings.Join(rest[1:], "."))}
			}
			fv.SetMapIndex(keyV, reflect.Value{}) // delete
			if fv.Len() == 0 {
				fv.Set(reflect.Zero(fv.Type()))
			}
			return nil
		}
		if elemT.Kind() == reflect.Struct {
			existing := fv.MapIndex(keyV)
			if !existing.IsValid() {
				return nil
			}
			slot := reflect.New(elemT).Elem()
			slot.Set(existing)
			if err := unsetInto(slot, fullPath, rest[1:]); err != nil {
				return err
			}
			fv.SetMapIndex(keyV, slot)
			return nil
		}
		return &Error{Kind: "not_scalar", Path: fullPath,
			Message: fmt.Sprintf("map entries of type %s are not patchable — use `edit`", elemT.Kind())}

	case reflect.Struct:
		if len(rest) == 0 {
			fv.Set(reflect.Zero(fv.Type()))
			return nil
		}
		child, remaining, err := lookupStructField(fv, fullPath, rest)
		if err != nil {
			return err
		}
		return unsetInto(child, fullPath, remaining)

	case reflect.Slice:
		return &Error{Kind: "list_not_supported", Path: fullPath,
			Message: "list fields are not patchable — use `edit` to modify lists"}

	case reflect.Ptr:
		if len(rest) == 0 {
			// Leaf pointer — clear to nil so omitempty round-trips cleanly.
			// Matches git-config `--unset`: the field goes away, it doesn't
			// just flip to its zero-pointed value.
			fv.Set(reflect.Zero(fv.Type()))
			return nil
		}
		if fv.IsNil() {
			return nil
		}
		return unsetInto(fv.Elem(), fullPath, rest)

	default:
		return &Error{Kind: "not_scalar", Path: fullPath,
			Message: fmt.Sprintf("unsupported field kind %s", fv.Kind())}
	}
}

// lookupStructField matches segs[0] against cur's yaml tags and
// returns the matched field plus the remaining segs.
func lookupStructField(cur reflect.Value, fullPath string, segs []string) (reflect.Value, []string, error) {
	if len(segs) == 0 {
		return reflect.Value{}, nil, &Error{Kind: "bad_path", Path: fullPath, Message: "empty path"}
	}
	if cur.Kind() != reflect.Struct {
		return reflect.Value{}, nil, &Error{Kind: "bad_path", Path: fullPath,
			Message: fmt.Sprintf("expected struct at %s, got %s", segs[0], cur.Kind())}
	}
	want := segs[0]
	t := cur.Type()
	var known []string
	for i := 0; i < t.NumField(); i++ {
		tag := yamlName(t.Field(i))
		if tag == "" || tag == "-" {
			continue
		}
		known = append(known, tag)
		if tag == want {
			return cur.Field(i), segs[1:], nil
		}
	}
	return reflect.Value{}, nil, &Error{
		Kind:    "unknown_field",
		Path:    fullPath,
		Message: fmt.Sprintf("unknown field %q", want),
		Suggest: nearestMatch(want, known),
	}
}

// yamlName strips the first comma-separated piece of a yaml tag.
// Fields without a yaml tag fall back to lowercased Go name so
// defaults still work.
func yamlName(f reflect.StructField) string {
	tag := f.Tag.Get("yaml")
	if tag == "" {
		return strings.ToLower(f.Name)
	}
	if i := strings.Index(tag, ","); i >= 0 {
		tag = tag[:i]
	}
	return tag
}

// setScalar converts value (string/bool/int/float) to the field's
// Go type and assigns it. Strings pass through; bools and ints get
// parsed from their string form if the caller passed a string
// (common from CLI argv).
func setScalar(fv reflect.Value, path string, value any) error {
	switch fv.Kind() {
	case reflect.String:
		s, ok := stringify(value)
		if !ok {
			return &Error{Kind: "type_mismatch", Path: path,
				Message: fmt.Sprintf("expected string, got %T", value)}
		}
		fv.SetString(s)
	case reflect.Bool:
		switch v := value.(type) {
		case bool:
			fv.SetBool(v)
		case string:
			b, err := strconv.ParseBool(v)
			if err != nil {
				return &Error{Kind: "type_mismatch", Path: path,
					Message: fmt.Sprintf("expected bool, got %q", v)}
			}
			fv.SetBool(b)
		default:
			return &Error{Kind: "type_mismatch", Path: path,
				Message: fmt.Sprintf("expected bool, got %T", value)}
		}
	case reflect.Int, reflect.Int64:
		switch v := value.(type) {
		case int:
			fv.SetInt(int64(v))
		case int64:
			fv.SetInt(v)
		case float64:
			fv.SetInt(int64(v))
		case string:
			n, err := strconv.ParseInt(v, 10, 64)
			if err != nil {
				return &Error{Kind: "type_mismatch", Path: path,
					Message: fmt.Sprintf("expected int, got %q", v)}
			}
			fv.SetInt(n)
		default:
			return &Error{Kind: "type_mismatch", Path: path,
				Message: fmt.Sprintf("expected int, got %T", value)}
		}
	default:
		return &Error{Kind: "type_mismatch", Path: path,
			Message: fmt.Sprintf("unsupported scalar kind %s", fv.Kind())}
	}
	return nil
}

func stringify(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case bool:
		return strconv.FormatBool(x), true
	case int:
		return strconv.Itoa(x), true
	case int64:
		return strconv.FormatInt(x, 10), true
	case float64:
		// JSON numbers decode to float64; accept integer-valued floats as strings.
		return strconv.FormatFloat(x, 'f', -1, 64), true
	default:
		return "", false
	}
}

// nearestMatch returns the closest entry in known by Levenshtein
// distance, but only if it's within a small threshold (3 edits or
// ≤30% of the longer string). Empty when no match is close enough.
func nearestMatch(want string, known []string) string {
	if len(known) == 0 {
		return ""
	}
	best := ""
	bestD := -1
	for _, k := range known {
		d := levenshtein(want, k)
		if bestD == -1 || d < bestD {
			best = k
			bestD = d
		}
	}
	limit := 3
	if l := len(want); l > 10 {
		limit = l * 3 / 10
	}
	if bestD > limit {
		return ""
	}
	return best
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = minInt(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}

func minInt(a, b, c int) int {
	m := a
	if b < m {
		m = b
	}
	if c < m {
		m = c
	}
	return m
}

// KnownPaths returns the complete set of dotted paths reachable on
// the given struct type, walking yaml tags. Used by renderers and
// tests to sanity-check coverage. For map fields (which accept any
// key) it returns the parent-path plus ".<key>" as a pseudo-leaf
// — callers format the `<key>` bit for their own use.
func KnownPaths(t reflect.Type) []string {
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}
	if t.Kind() != reflect.Struct {
		return nil
	}
	var out []string
	var walk func(prefix string, t reflect.Type)
	walk = func(prefix string, t reflect.Type) {
		for i := 0; i < t.NumField(); i++ {
			f := t.Field(i)
			tag := yamlName(f)
			if tag == "" || tag == "-" {
				continue
			}
			p := tag
			if prefix != "" {
				p = prefix + "." + tag
			}
			switch f.Type.Kind() {
			case reflect.Struct:
				walk(p, f.Type)
			case reflect.Map:
				out = append(out, p+".<key>")
			case reflect.Slice:
				// Lists aren't patchable — don't surface to renderers.
			default:
				out = append(out, p)
			}
		}
	}
	walk("", t)
	sort.Strings(out)
	return out
}
