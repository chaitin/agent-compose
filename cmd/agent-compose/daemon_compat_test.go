package main

import (
	"net"
	"os"
)

func isTrustedUnixSocketConn(conn net.Conn) bool {
	unixConn, ok := conn.(*net.UnixConn)
	if !ok {
		return false
	}
	uid, err := unixSocketPeerUID(unixConn)
	if err != nil {
		return false
	}
	return uid == os.Getuid() || uid == 0
}
