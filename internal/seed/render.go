package seed

import (
	"bytes"
	"fmt"
	"text/template"
)

// Render substitutes the template's Path and Content fields against
// vars and returns one RenderedFile per File entry. Errors include the
// template name + file index so failures point at the offending entry.
func Render(t *Template, vars Vars) ([]RenderedFile, error) {
	if t == nil {
		return nil, nil
	}
	out := make([]RenderedFile, 0, len(t.Files))
	for i, f := range t.Files {
		path, err := renderString(fmt.Sprintf("%s.files[%d].path", t.Name, i), f.Path, vars)
		if err != nil {
			return nil, err
		}
		content, err := renderString(fmt.Sprintf("%s.files[%d].content", t.Name, i), f.Content, vars)
		if err != nil {
			return nil, err
		}
		mode := f.OnConflict
		if mode == "" {
			mode = ConflictOverwrite
		}
		if mode == ConflictMerge {
			if _, err := MergeFormatFromPath(path); err != nil {
				return nil, fmt.Errorf("%s.files[%d]: %w", t.Name, i, err)
			}
		}
		out = append(out, RenderedFile{
			Path:          path,
			Content:       []byte(content),
			OnConflict:    mode,
			BreakSymlinks: f.BreakSymlinks,
		})
	}
	return out, nil
}

func renderString(label, src string, vars Vars) (string, error) {
	tmpl, err := template.New(label).Option("missingkey=zero").Parse(src)
	if err != nil {
		return "", fmt.Errorf("parse %s: %w", label, err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("execute %s: %w", label, err)
	}
	return buf.String(), nil
}
