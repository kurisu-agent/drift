package drift

import "os"

// userHome is swappable so the txtar harness can point HOME at its
// workdir via env.Setenv.
var userHome = func() (string, error) { return os.UserHomeDir() }
