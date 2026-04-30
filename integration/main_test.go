//go:build integration

package integration

import (
	"fmt"
	"os"
	"testing"
)

// TestMain builds drift + lakitu once for the whole package and tears the
// shared tmp dir down after m.Run. Individual tests rely on driftBinary /
// lakituBinary which gate on the Once guards in harness.go.
//
// Must live in a *_test.go file — `go test` only recognizes TestMain from
// test files. A TestMain in harness.go compiles but never runs, leaving
// pkgTmpDir empty and cascading into "open lakitu: no such file" when
// buildImage stages a relative path from the wrong cwd.
func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "drift-integration-")
	if err != nil {
		fmt.Fprintf(os.Stderr, "TestMain: mkdir temp: %v\n", err)
		os.Exit(1)
	}
	pkgTmpDir = dir
	code := m.Run()
	_ = os.RemoveAll(pkgTmpDir)
	os.Exit(code)
}
