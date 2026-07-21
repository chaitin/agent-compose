package core

import (
	"fmt"
	"net"
	"strings"
)

// hostAddress reports the address an operator should point a browser at. The
// installer usually runs on a server, so "localhost" is almost never the answer.
//
// Dialing UDP sends no packets and needs no reachable peer; the kernel only
// consults the routing table to pick the interface a packet would leave from,
// which is exactly the address remote clients can reach. Unlike walking
// net.Interfaces() it also skips docker0 and other virtual bridges without
// maintaining a name blocklist.
func hostAddress() string {
	connection, err := net.Dial("udp", "8.8.8.8:80")
	if err != nil {
		return "localhost"
	}
	// The socket is unconnected in any meaningful sense; a close failure cannot
	// affect the address already read from it.
	defer func() { _ = connection.Close() }()
	address, ok := connection.LocalAddr().(*net.UDPAddr)
	if !ok || address.IP == nil || address.IP.IsUnspecified() {
		return "localhost"
	}
	return address.IP.String()
}

func httpURL(host string, port int) string {
	if strings.Contains(host, ":") {
		host = "[" + host + "]"
	}
	if port == 80 {
		return "http://" + host
	}
	return fmt.Sprintf("http://%s:%d", host, port)
}
