//go:build !linux && !darwin

package workspaces

import (
	"errors"
	"os"
)

func tryLockTransientSnapshotFile(*os.File) (bool, error) { return false, errors.ErrUnsupported }
