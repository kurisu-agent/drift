package drift

import "os"

// userHome wraps os.UserHomeDir so tests can swap the behavior — in
// particular, the txtar harness sets HOME to the script workdir via
// env.Setenv, and os.UserHomeDir honors that on Linux.
var userHome = func() (string, error) { return os.UserHomeDir() }
