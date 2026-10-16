//go:build !linux

package driver

import "errors"

// errBoxliteShimReapUnsupported reports that the platform has no boxlite driver
// to reap for. The driver itself only builds on linux, so nothing reaches this,
// but the shared reaper must still compile for the other build targets.
var errBoxliteShimReapUnsupported = errors.New("reaping boxlite shim processes is unsupported on this platform")

func identifyBoxliteShimProcess(int) (bool, error) { return false, errBoxliteShimReapUnsupported }

func terminateBoxliteShim(int) error { return errBoxliteShimReapUnsupported }

func boxliteProcessAlive(int) bool { return false }
