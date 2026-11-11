//go:build linux

package sandboxinit

import (
	"bufio"
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const dnsSocketCacheLimit = 4096

// Inodes provide the cheap prefilter; the kernel cookie validates the actual
// socket after duplication, including across fork and descriptor reuse.
type dnsSocketDestination struct {
	raw    []byte
	cookie uint64
}

func dnsSocketIdentity(fd int) (uint64, uint64, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return 0, 0, err
	}
	domain, err := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_DOMAIN)
	if err != nil {
		return 0, 0, err
	}
	if domain != unix.AF_INET && domain != unix.AF_INET6 {
		return 0, 0, unix.EAFNOSUPPORT
	}
	cookie, err := unix.GetsockoptUint64(fd, unix.SOL_SOCKET, unix.SO_COOKIE)
	return stat.Ino, cookie, err
}

// Live mappings never expire: a connected resolver may send without a name
// and remain idle indefinitely. Refuse a new redirect if all slots are live.
func (s *egressSupervisor) rememberDNSSocket(fd int, raw []byte) error {
	inode, cookie, err := dnsSocketIdentity(fd)
	if err != nil {
		return err
	}
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	if _, exists := s.dnsSockets[inode]; !exists && len(s.dnsSockets) >= dnsSocketCacheLimit {
		if err := s.pruneDNSSockets(); err != nil {
			return err
		}
		if len(s.dnsSockets) >= dnsSocketCacheLimit {
			return unix.ENOSPC
		}
	}
	s.dnsSockets[inode] = dnsSocketDestination{raw: bytes.Clone(raw), cookie: cookie}
	return nil
}

// The workload cannot change network namespaces (the filter denies setns and
// unshare). Kernel inet tables retain live bound sockets regardless of which
// process owns their fd. Init's own fds also cover redirects not yet connected
// and canceled receives. Scan only at capacity, while holding dnsMu.
func (s *egressSupervisor) pruneDNSSockets() error {
	live := make(map[uint64]bool)
	for _, protocol := range []string{"udp", "udp6", "tcp", "tcp6"} {
		file, err := os.Open("/proc/self/net/" + protocol)
		if os.IsNotExist(err) && strings.HasSuffix(protocol, "6") {
			continue
		}
		if err != nil {
			return err
		}
		scanner := bufio.NewScanner(file)
		scanner.Scan() // header
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 10 {
				_ = file.Close()
				return fmt.Errorf("invalid %s socket table", protocol)
			}
			inode, err := strconv.ParseUint(fields[9], 10, 64)
			if err != nil {
				_ = file.Close()
				return err
			}
			live[inode] = true
		}
		err = scanner.Err()
		_ = file.Close()
		if err != nil {
			return err
		}
	}
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return err
	}
	for _, fd := range fds {
		link, err := os.Readlink("/proc/self/fd/" + fd.Name())
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		inode, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]"), 10, 64)
		if err == nil {
			live[inode] = true
		}
	}
	for inode := range s.dnsSockets {
		if !live[inode] {
			delete(s.dnsSockets, inode)
		}
	}
	return nil
}

func (s *egressSupervisor) dnsSocketSource(fd int) ([]byte, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	entry, ok := s.dnsSockets[stat.Ino]
	if !ok {
		return nil, nil
	}
	_, cookie, err := dnsSocketIdentity(fd)
	if errors.Is(err, unix.EAFNOSUPPORT) || err == nil && cookie != entry.cookie {
		delete(s.dnsSockets, stat.Ino)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return bytes.Clone(entry.raw), nil
}

func rawDNSReplySource(from unix.Sockaddr) []byte {
	switch from := from.(type) {
	case *unix.SockaddrInet4:
		raw, _ := sockaddrFromTCPAddr(&net.TCPAddr{IP: net.IP(from.Addr[:]), Port: from.Port})
		return raw
	case *unix.SockaddrInet6:
		raw := make([]byte, 28)
		binary.NativeEndian.PutUint16(raw, unix.AF_INET6)
		binary.BigEndian.PutUint16(raw[2:], uint16(from.Port))
		copy(raw[8:24], from.Addr[:])
		binary.NativeEndian.PutUint32(raw[24:], from.ZoneId)
		return raw
	default:
		return nil
	}
}

func restoreDNSReplySource(original, actual []byte) []byte {
	peer, ok := tcpAddrFromSockaddr(actual)
	if ok && peer.Port == 53 && (peer.IP.Equal(net.IPv4(127, 0, 0, 1)) || peer.IP.Equal(net.IPv6loopback)) {
		return original
	}
	return actual
}
