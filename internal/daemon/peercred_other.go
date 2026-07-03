//go:build !linux && !darwin

package daemon

import (
	"fmt"
	"net"
	"runtime"
)

func unixSocketPeerUID(_ *net.UnixConn) (int, error) {
	return 0, fmt.Errorf("unix socket peer credentials not supported on %s", runtime.GOOS)
}
