package run

// MergeBuiltinDefaults back-fills args (and other prompt-side metadata)
// onto entries in user that match a defaults entry by name AND command.
//
// Why this exists: runs.yaml is seeded once by lakitu init. Later schema
// additions — in particular the args: prompt declarations introduced in
// v0.5.2 — never reach circuits that were initialized before the feature
// shipped, because EnsureRunsYAML deliberately preserves user edits. The
// symptom is `drift run ping` skipping the prompt it would otherwise
// render, because the server returns an entry with no args to prompt for.
//
// The command equality gate is the safety net: if the user has modified
// the command, we can no longer assume our embedded arg shape matches
// their template. In that case we leave the entry alone and the user
// owns re-declaring their args. Only the "never-touched" built-in entries
// get the back-fill.
func MergeBuiltinDefaults(user, defaults *Registry) {
	if user == nil || defaults == nil {
		return
	}
	for name, def := range defaults.Entries {
		got, ok := user.Entries[name]
		if !ok {
			continue
		}
		if got.Command != def.Command {
			continue
		}
		if len(got.Args) == 0 && len(def.Args) > 0 {
			got.Args = def.Args
		}
		user.Entries[name] = got
	}
}
