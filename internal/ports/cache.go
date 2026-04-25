package ports

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/kurisu-agent/drift/internal/config"
)

// liveCache is reconcile's record of forwards it has *successfully*
// installed on each master. ssh has no `-O list` verb, so without this
// we'd have to either re-issue every forward (errors on duplicate) or
// drop and re-add (briefly tears down a working tunnel). The cache lets
// us compute add/cancel deltas straight from the desired state.
//
// On disk: ~/.config/drift/forwards.json. Wiped per-host when
// `ssh -O check` reports the master gone.
type liveCache struct {
	Hosts map[string]liveHost `json:"hosts,omitempty"`
}

type liveHost struct {
	Forwards []livePair `json:"forwards,omitempty"`
}

type livePair struct {
	Local  int `json:"local"`
	Remote int `json:"remote"`
}

func livePath() (string, error) {
	dir, err := config.ClientConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "forwards.json"), nil
}

func loadLiveCache(path string) (*liveCache, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return &liveCache{Hosts: map[string]liveHost{}}, nil
		}
		return nil, fmt.Errorf("ports: read %s: %w", path, err)
	}
	var c liveCache
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("ports: parse %s: %w", path, err)
	}
	if c.Hosts == nil {
		c.Hosts = map[string]liveHost{}
	}
	return &c, nil
}

func saveLiveCache(path string, c *liveCache) error {
	// Drop empty host slots so the file stays readable.
	for h, lh := range c.Hosts {
		if len(lh.Forwards) == 0 {
			delete(c.Hosts, h)
		}
	}
	for h, lh := range c.Hosts {
		sort.Slice(lh.Forwards, func(i, j int) bool {
			if lh.Forwards[i].Remote != lh.Forwards[j].Remote {
				return lh.Forwards[i].Remote < lh.Forwards[j].Remote
			}
			return lh.Forwards[i].Local < lh.Forwards[j].Local
		})
		c.Hosts[h] = lh
	}
	buf, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("ports: marshal cache: %w", err)
	}
	buf = append(buf, '\n')
	return config.WriteFileAtomic(path, buf, 0o600)
}

func (c *liveCache) get(host string) []livePair {
	if c == nil || c.Hosts == nil {
		return nil
	}
	return c.Hosts[host].Forwards
}

func (c *liveCache) set(host string, pairs []livePair) {
	if c.Hosts == nil {
		c.Hosts = map[string]liveHost{}
	}
	if len(pairs) == 0 {
		delete(c.Hosts, host)
		return
	}
	c.Hosts[host] = liveHost{Forwards: pairs}
}

func livePairsFromForwards(fwds []Forward) []livePair {
	out := make([]livePair, len(fwds))
	for i, f := range fwds {
		out[i] = livePair{Local: f.Local, Remote: f.Remote}
	}
	return out
}

// diffPairs returns the pairs in desired but not in current (add) and
// the pairs in current but not in desired (cancel). Equality is on both
// Local and Remote so a remap (Local change) shows as cancel+add.
func diffPairs(current, desired []livePair) (add, cancel []livePair) {
	in := func(set []livePair, p livePair) bool {
		for _, q := range set {
			if q == p {
				return true
			}
		}
		return false
	}
	for _, p := range desired {
		if !in(current, p) {
			add = append(add, p)
		}
	}
	for _, p := range current {
		if !in(desired, p) {
			cancel = append(cancel, p)
		}
	}
	return add, cancel
}
