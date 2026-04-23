package yamlpath

import (
	"errors"
	"reflect"
	"testing"
)

type tuneLike struct {
	Starter      string            `yaml:"starter,omitempty"`
	Devcontainer string            `yaml:"devcontainer,omitempty"`
	Env          tuneEnvLike       `yaml:"env,omitempty"`
	MountDirs    []mountLike       `yaml:"mount_dirs,omitempty"`
	Extras       map[string]string `yaml:"extras,omitempty"`
	Autostart    bool              `yaml:"autostart,omitempty"`
}

type tuneEnvLike struct {
	Build     map[string]string `yaml:"build,omitempty"`
	Workspace map[string]string `yaml:"workspace,omitempty"`
}

type mountLike struct {
	Source string `yaml:"source,omitempty"`
	Target string `yaml:"target,omitempty"`
}

func TestSetScalarField(t *testing.T) {
	v := tuneLike{}
	if err := Apply(&v, []Op{{Path: "starter", Op: OpSet, Value: "https://example.org/foo"}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if v.Starter != "https://example.org/foo" {
		t.Fatalf("starter = %q", v.Starter)
	}
}

func TestSetBoolField(t *testing.T) {
	v := tuneLike{}
	if err := Apply(&v, []Op{{Path: "autostart", Op: OpSet, Value: "true"}}); err != nil {
		t.Fatalf("apply bool from string: %v", err)
	}
	if !v.Autostart {
		t.Fatalf("autostart should be true")
	}
	if err := Apply(&v, []Op{{Path: "autostart", Op: OpSet, Value: false}}); err != nil {
		t.Fatalf("apply bool direct: %v", err)
	}
	if v.Autostart {
		t.Fatalf("autostart should be false")
	}
}

func TestSetMapEntryAutoCreates(t *testing.T) {
	v := tuneLike{}
	if err := Apply(&v, []Op{{Path: "env.build.GITHUB_TOKEN", Op: OpSet, Value: "chest:foo"}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if v.Env.Build["GITHUB_TOKEN"] != "chest:foo" {
		t.Fatalf("env.build.GITHUB_TOKEN = %v", v.Env.Build)
	}
}

func TestUnsetMapEntryPrunes(t *testing.T) {
	v := tuneLike{
		Env: tuneEnvLike{Build: map[string]string{"GITHUB_TOKEN": "chest:foo"}},
	}
	if err := Apply(&v, []Op{{Path: "env.build.GITHUB_TOKEN", Op: OpUnset}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if v.Env.Build != nil {
		t.Fatalf("expected Build pruned to nil after removing last key, got %v", v.Env.Build)
	}
}

func TestUnsetMapEntryKeepsOtherKeys(t *testing.T) {
	v := tuneLike{
		Env: tuneEnvLike{Build: map[string]string{
			"A": "chest:a",
			"B": "chest:b",
		}},
	}
	if err := Apply(&v, []Op{{Path: "env.build.A", Op: OpUnset}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, ok := v.Env.Build["A"]; ok {
		t.Fatalf("A should be gone")
	}
	if v.Env.Build["B"] != "chest:b" {
		t.Fatalf("B should remain")
	}
}

func TestUnsetScalarZeros(t *testing.T) {
	v := tuneLike{Starter: "https://example.org/foo"}
	if err := Apply(&v, []Op{{Path: "starter", Op: OpUnset}}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if v.Starter != "" {
		t.Fatalf("starter should be zeroed")
	}
}

func TestUnknownFieldHintsNearest(t *testing.T) {
	v := tuneLike{}
	err := Apply(&v, []Op{{Path: "env.buld.X", Op: OpSet, Value: "chest:x"}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var pe *Error
	if !errors.As(err, &pe) {
		t.Fatalf("want *Error, got %T", err)
	}
	if pe.Kind != "unknown_field" {
		t.Fatalf("kind = %q", pe.Kind)
	}
	if pe.Suggest != "build" {
		t.Fatalf("suggest = %q, want %q", pe.Suggest, "build")
	}
}

func TestSetOnListFieldErrors(t *testing.T) {
	v := tuneLike{}
	err := Apply(&v, []Op{{Path: "mount_dirs", Op: OpSet, Value: "whatever"}})
	if err == nil {
		t.Fatalf("expected error on list set")
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.Kind != "list_not_supported" {
		t.Fatalf("want list_not_supported, got %v", err)
	}
}

func TestDescendingPastScalarErrors(t *testing.T) {
	v := tuneLike{}
	err := Apply(&v, []Op{{Path: "starter.foo", Op: OpSet, Value: "x"}})
	if err == nil {
		t.Fatalf("expected error")
	}
	var pe *Error
	if !errors.As(err, &pe) || pe.Kind != "not_scalar" {
		t.Fatalf("want not_scalar, got %v", err)
	}
}

func TestOpsAppliedInOrder(t *testing.T) {
	v := tuneLike{}
	ops := []Op{
		{Path: "env.build.A", Op: OpSet, Value: "chest:a"},
		{Path: "env.build.B", Op: OpSet, Value: "chest:b"},
		{Path: "env.build.A", Op: OpUnset},
	}
	if err := Apply(&v, ops); err != nil {
		t.Fatalf("apply: %v", err)
	}
	if _, ok := v.Env.Build["A"]; ok {
		t.Fatalf("A should be gone")
	}
	if v.Env.Build["B"] != "chest:b" {
		t.Fatalf("B should remain: %v", v.Env.Build)
	}
}

func TestKnownPathsEnumerates(t *testing.T) {
	got := KnownPaths(reflect.TypeOf(tuneLike{}))
	want := []string{
		"autostart",
		"devcontainer",
		"env.build.<key>",
		"env.workspace.<key>",
		"extras.<key>",
		"starter",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("KnownPaths = %v\nwant %v", got, want)
	}
}

func TestApplyRejectsNonPointer(t *testing.T) {
	if err := Apply(tuneLike{}, nil); err == nil {
		t.Fatalf("expected error")
	}
}
