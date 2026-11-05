//go:build !linux

package driver

import (
	"errors"
	"time"
)

// startSupervisorProcess reports that the independent supervisor isolation mode
// is Linux-only. Docker on macOS keeps the historical process and network
// model, so the mode fails closed instead of silently dropping its guarantees.
func startSupervisorProcess(dockerSupervisorStart) (*dockerSupervisorProcess, error) {
	return nil, errors.New("the independent sandbox supervisor is only supported on Linux")
}

func stopSupervisorProcess(*dockerSupervisorProcess, time.Duration) error {
	return nil
}
