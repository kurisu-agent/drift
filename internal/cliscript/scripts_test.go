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
		Setup: func(env *testscript.Env) error {
			// testscript defaults HOME to /no-home so buggy code that writes
			// under the real user's home fails visibly. We want a real
			// writable $HOME per test — point it at WorkDir so `lakitu init`
			// and XDG-path helpers both have somewhere to operate.
			env.Setenv("HOME", env.WorkDir)
			return nil
		},
	})
}
