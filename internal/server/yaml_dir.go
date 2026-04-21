package server

import (
	"errors"
	"io/fs"
	"os"
	"sort"
	"strings"
)

// listYAMLNames returns sorted entry names (without the `.yaml` suffix) for
// every `<name>.yaml` file in dir. Missing dir → (nil, nil) so handlers can
// return an empty list on a freshly-initialized garage instead of a 500.
// Non-yaml entries and subdirectories are silently skipped — this is the
// single source of truth for the handful of tune/character/… directories
// that share the "one yaml file per entity" convention.
func listYAMLNames(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		out = append(out, strings.TrimSuffix(e.Name(), ".yaml"))
	}
	sort.Strings(out)
	return out, nil
}
