package cliscript_test

import (
	"testing"

	"github.com/kurisu-agent/drift/internal/cliscript"
	"github.com/rogpeppe/go-internal/testscript"
)

func TestMain(m *testing.M) {
	testscript.Main(m, cliscript.Commands())
}

func TestScripts(t *testing.T) {
	testscript.Run(t, testscript.Params{
		Dir: "testdata/scripts",
	})
}
