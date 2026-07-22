//go:build !unix

package core

import "os/exec"

// cancelProcessTree has no portable equivalent outside unix, so cancellation
// keeps the default behaviour of signalling only the process that was started.
// A descendant holding the output pipe therefore delays Wait until WaitDelay
// elapses instead of being stopped outright. The installer only ships unix
// binaries; this exists so the package still builds elsewhere.
func cancelProcessTree(*exec.Cmd) {}
