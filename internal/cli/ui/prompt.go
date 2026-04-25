package ui

import (
	"errors"

	"charm.land/huh/v2"
)

// ErrAborted is returned when the user dismisses a prompt (Ctrl-C, Esc).
var ErrAborted = errors.New("user aborted")

// ConfirmOptions configures a yes/no prompt.
type ConfirmOptions struct {
	Title       string
	Description string
	Default     bool
	Affirmative string // overrides "Yes"
	Negative    string // overrides "No"
}

// Confirm runs a yes/no prompt. Returns false + ErrAborted if the user
// dismissed without answering.
func Confirm(o ConfirmOptions) (bool, error) {
	val := o.Default
	c := huh.NewConfirm().
		Title(o.Title).
		Value(&val)
	if o.Description != "" {
		c = c.Description(o.Description)
	}
	if o.Affirmative != "" {
		c = c.Affirmative(o.Affirmative)
	}
	if o.Negative != "" {
		c = c.Negative(o.Negative)
	}
	if err := c.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return false, ErrAborted
		}
		return false, err
	}
	return val, nil
}

// SelectOption is one row in a Select prompt.
type SelectOption[T comparable] struct {
	Label string
	Value T
}

// SelectOptions configures a single-select picker.
type SelectOptions[T comparable] struct {
	Title       string
	Description string
	Options     []SelectOption[T]
	Default     T
	Filter      bool
	Height      int
}

// Select runs a single-choice picker. Returns the zero value + ErrAborted
// on dismissal.
func Select[T comparable](o SelectOptions[T]) (T, error) {
	var zero T
	val := o.Default
	hopts := make([]huh.Option[T], len(o.Options))
	for i, x := range o.Options {
		hopts[i] = huh.NewOption(x.Label, x.Value)
	}
	s := huh.NewSelect[T]().
		Title(o.Title).
		Options(hopts...).
		Value(&val).
		Filtering(o.Filter)
	if o.Description != "" {
		s = s.Description(o.Description)
	}
	if o.Height > 0 {
		s = s.Height(o.Height)
	}
	if err := s.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return zero, ErrAborted
		}
		return zero, err
	}
	return val, nil
}

// InputOptions configures a text-input prompt.
type InputOptions struct {
	Title       string
	Description string
	Placeholder string
	Default     string
	Validate    func(string) error
	Password    bool
	CharLimit   int
}

// Input runs a free-form text prompt. Empty + ErrAborted on dismissal.
func Input(o InputOptions) (string, error) {
	val := o.Default
	in := huh.NewInput().
		Title(o.Title).
		Value(&val)
	if o.Description != "" {
		in = in.Description(o.Description)
	}
	if o.Placeholder != "" {
		in = in.Placeholder(o.Placeholder)
	}
	if o.Validate != nil {
		in = in.Validate(o.Validate)
	}
	if o.Password {
		in = in.EchoMode(huh.EchoModePassword)
	}
	if o.CharLimit > 0 {
		in = in.CharLimit(o.CharLimit)
	}
	if err := in.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return "", ErrAborted
		}
		return "", err
	}
	return val, nil
}

// Pick is a small generic helper used by drift's cross-circuit and
// merged kart pickers. opts.ToOption maps each entry to its (label,
// description) pair; empty descriptions hide the second column.
type PickOptions[T any] struct {
	Title       string
	Description string
	Items       []T
	Format      func(T) string
	Filter      bool
	Height      int
}

// Pick runs a Select over Items, where Format yields the visible label.
// Returns the chosen item plus its index, or zero/-1 + ErrAborted.
func Pick[T any](o PickOptions[T]) (T, int, error) {
	var zero T
	if len(o.Items) == 0 {
		return zero, -1, errors.New("no items")
	}
	type idx int
	hopts := make([]huh.Option[idx], len(o.Items))
	for i, it := range o.Items {
		hopts[i] = huh.NewOption(o.Format(it), idx(i))
	}
	var sel idx
	s := huh.NewSelect[idx]().
		Title(o.Title).
		Options(hopts...).
		Value(&sel).
		Filtering(o.Filter)
	if o.Description != "" {
		s = s.Description(o.Description)
	}
	if o.Height > 0 {
		s = s.Height(o.Height)
	}
	if err := s.Run(); err != nil {
		if errors.Is(err, huh.ErrUserAborted) {
			return zero, -1, ErrAborted
		}
		return zero, -1, err
	}
	return o.Items[int(sel)], int(sel), nil
}
