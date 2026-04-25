package ui

import (
	"fmt"
	"io"
	"strings"
)

// KeyValuePair is one row of a two-column key-value rendering.
type KeyValuePair struct {
	Key, Value string
}

// KeyValue writes pairs as "  key: value" with the key dim and the value
// in the default fg. Used by drift kart info, dashboard kart row expand,
// and other detail views.
func (t *Theme) KeyValue(w io.Writer, pairs []KeyValuePair) {
	if len(pairs) == 0 {
		return
	}
	width := 0
	for _, p := range pairs {
		if n := len(p.Key); n > width {
			width = n
		}
	}
	for _, p := range pairs {
		key := p.Key + strings.Repeat(" ", width-len(p.Key))
		if t != nil && t.Enabled {
			key = t.DimStyle.Render(key)
		}
		fmt.Fprintf(w, "  %s  %s\n", key, p.Value)
	}
}
