package driver

import (
	"errors"
	"fmt"
	"time"
)

// ErrExecTerminationUnconfirmed means a runtime driver returned without being
// able to prove that the guest execution it started has stopped.
var ErrExecTerminationUnconfirmed = errors.New("guest execution termination unconfirmed")

const execTerminationTimeout = 15 * time.Second

type execTerminationUnconfirmedError struct {
	driver      string
	executionID string
	cause       error
}

func (e *execTerminationUnconfirmedError) Error() string {
	return fmt.Sprintf("%s runtime could not confirm termination of guest execution %s: %v", e.driver, e.executionID, e.cause)
}

func (e *execTerminationUnconfirmedError) Unwrap() error {
	return e.cause
}

func (e *execTerminationUnconfirmedError) Is(target error) bool {
	return target == ErrExecTerminationUnconfirmed
}

func execTerminationResultError(driver, executionID string, primary, terminationErr error) error {
	if terminationErr == nil {
		return primary
	}
	unconfirmed := &execTerminationUnconfirmedError{
		driver:      driver,
		executionID: executionID,
		cause:       terminationErr,
	}
	if primary == nil {
		return unconfirmed
	}
	return errors.Join(primary, unconfirmed)
}
