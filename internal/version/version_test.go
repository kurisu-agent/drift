package version_test

import (
	"testing"

	"github.com/kurisu-agent/drift/internal/version"
)

func TestGet_alwaysReturnsVersion(t *testing.T) {
	info := version.Get()
	if info.Version == "" {
		t.Error("Version must not be empty — should fall back to 'devel'")
	}
	if info.APISchema < 1 {
		t.Errorf("APISchema = %d, want >= 1", info.APISchema)
	}
}
