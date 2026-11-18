//go:build !unix

package runinputs

import "errors"

func freeSpace(string) (uint64, uint64, error) {
	return 0, 0, errors.ErrUnsupported
}
