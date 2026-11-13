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
	"time"

	"golang.org/x/sys/unix"
)

const (
	dnsSocketCacheLimit = 4096
	// Reclaiming closed sockets is an optimization, not a requirement for
	// progress. Rate-limit the /proc scan so a workload that fills every slot
	// cannot force one scan per DNS redirect on the notification goroutine.
	dnsSocketPruneInterval = time.Second
)

// Inodes provide the cheap prefilter; the kernel cookie validates the actual
// socket after duplication, including across fork and descriptor reuse.
type dnsSocketDestination struct {
	raw      []byte
	cookie   uint64
	lastUsed uint64
}

// dnsSocketSnapshot fingerprints one mapping so a first-time prune cannot
// delete an entry that another notification refreshed while /proc was scanned.
type dnsSocketSnapshot struct {
	cookie   uint64
	lastUsed uint64
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

// Live mappings never expire: a connected resolver may send without a name and
// remain idle indefinitely. The cache stays bounded instead of expiring active
// sockets. When every slot is live, the least recently used mapping is evicted
// and the new redirect proceeds. Refusing the redirect would turn cache
// pressure into a sandbox-wide DNS outage; an evicted socket only loses source
// restoration until a later redirect records it again.
func (s *egressSupervisor) rememberDNSSocket(fd int, raw []byte) error {
	inode, cookie, err := dnsSocketIdentity(fd)
	if err != nil {
		return err
	}
	s.dnsMu.Lock()
	_, exists := s.dnsSockets[inode]
	if exists || len(s.dnsSockets) < dnsSocketCacheLimit {
		s.storeDNSSocketLocked(inode, cookie, raw)
		s.dnsMu.Unlock()
		return nil
	}
	prune := time.Since(s.dnsLastPrune) >= dnsSocketPruneInterval
	if prune {
		s.dnsLastPrune = time.Now()
	}
	s.dnsMu.Unlock()

	// Reclaim closed sockets before giving up a live mapping. Scanning /proc
	// opens files, so it runs outside dnsMu; concurrent inserts are rechecked.
	if prune {
		if err := s.pruneDNSSockets(); err != nil && s.logger != nil {
			s.logger.Debug("sandbox init could not reclaim closed DNS sockets",
				"sandbox_id", s.sandboxID, "error", err)
		}
	}
	var evicted bool
	s.dnsMu.Lock()
	if _, exists := s.dnsSockets[inode]; !exists && len(s.dnsSockets) >= dnsSocketCacheLimit {
		evicted = s.evictOldestDNSSocketLocked()
	}
	s.storeDNSSocketLocked(inode, cookie, raw)
	s.dnsMu.Unlock()
	if evicted {
		s.warnDNSSocketCapacity()
	}
	return nil
}

// storeDNSSocketLocked records or refreshes one mapping. The caller must hold
// dnsMu.
func (s *egressSupervisor) storeDNSSocketLocked(inode, cookie uint64, raw []byte) {
	s.dnsSocketClock++
	s.dnsSockets[inode] = dnsSocketDestination{
		raw:      bytes.Clone(raw),
		cookie:   cookie,
		lastUsed: s.dnsSocketClock,
	}
}

// evictOldestDNSSocketLocked frees one slot for a new redirect after closed
// sockets have been reclaimed. The caller must hold dnsMu.
func (s *egressSupervisor) evictOldestDNSSocketLocked() bool {
	var (
		oldestInode uint64
		oldestUsed  uint64
		found       bool
	)
	for inode, entry := range s.dnsSockets {
		if !found || entry.lastUsed < oldestUsed || entry.lastUsed == oldestUsed && inode < oldestInode {
			oldestInode, oldestUsed, found = inode, entry.lastUsed, true
		}
	}
	if !found {
		return false
	}
	delete(s.dnsSockets, oldestInode)
	return true
}

// warnDNSSocketCapacity reports the first live eviction for one supervisor.
// Repeating the warning for every eviction would let a workload flood the log.
func (s *egressSupervisor) warnDNSSocketCapacity() {
	s.dnsMu.Lock()
	warned := s.dnsCapacityWarned
	s.dnsCapacityWarned = true
	s.dnsMu.Unlock()
	if warned || s.logger == nil {
		return
	}
	s.logger.Warn("sandbox init DNS socket cache is full; evicting the least recently used mapping",
		"sandbox_id", s.sandboxID, "limit", dnsSocketCacheLimit)
}

// The workload cannot change network namespaces (the filter denies setns and
// unshare). Kernel inet tables retain live bound sockets regardless of which
// process owns their fd. Init's own fds also cover redirects not yet connected
// and canceled receives. Scans run outside dnsMu because they open /proc files.
func (s *egressSupervisor) pruneDNSSockets() error {
	s.dnsMu.Lock()
	cached := make(map[uint64]dnsSocketSnapshot, len(s.dnsSockets))
	for inode, entry := range s.dnsSockets {
		cached[inode] = dnsSocketSnapshot{cookie: entry.cookie, lastUsed: entry.lastUsed}
	}
	s.dnsMu.Unlock()
	if len(cached) == 0 {
		return nil
	}

	live, err := liveDNSSocketInodes(cached)
	if err != nil {
		return err
	}
	s.dnsMu.Lock()
	defer s.dnsMu.Unlock()
	for inode, snapshot := range cached {
		entry, ok := s.dnsSockets[inode]
		if !ok || entry.cookie != snapshot.cookie || entry.lastUsed != snapshot.lastUsed {
			// A concurrent redirect refreshed or replaced this mapping.
			continue
		}
		if _, ok := live[inode]; !ok {
			delete(s.dnsSockets, inode)
		}
	}
	return nil
}

// liveDNSSocketInodes records only the cached inodes that are still live, so
// its allocation is bounded by the cache rather than by the socket table.
func liveDNSSocketInodes(wanted map[uint64]dnsSocketSnapshot) (map[uint64]struct{}, error) {
	live := make(map[uint64]struct{}, len(wanted))
	for _, protocol := range []string{"udp", "udp6", "tcp", "tcp6"} {
		file, err := os.Open("/proc/self/net/" + protocol)
		if os.IsNotExist(err) && strings.HasSuffix(protocol, "6") {
			continue
		}
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(file)
		scanner.Scan() // header
		for scanner.Scan() {
			fields := strings.Fields(scanner.Text())
			if len(fields) < 10 {
				_ = file.Close()
				return nil, fmt.Errorf("invalid %s socket table", protocol)
			}
			inode, err := strconv.ParseUint(fields[9], 10, 64)
			if err != nil {
				_ = file.Close()
				return nil, err
			}
			if _, ok := wanted[inode]; ok {
				live[inode] = struct{}{}
			}
		}
		err = scanner.Err()
		_ = file.Close()
		if err != nil {
			return nil, err
		}
	}
	fds, err := os.ReadDir("/proc/self/fd")
	if err != nil {
		return nil, err
	}
	for _, fd := range fds {
		link, err := os.Readlink("/proc/self/fd/" + fd.Name())
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		inode, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]"), 10, 64)
		if err == nil {
			if _, ok := wanted[inode]; ok {
				live[inode] = struct{}{}
			}
		}
	}
	return live, nil
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
	s.dnsSocketClock++
	entry.lastUsed = s.dnsSocketClock
	s.dnsSockets[stat.Ino] = entry
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

func isDNSStubSource(actual []byte) bool {
	peer, ok := tcpAddrFromSockaddr(actual)
	return ok && peer.Port == 53 && (peer.IP.Equal(net.IPv4(127, 0, 0, 1)) || peer.IP.Equal(net.IPv6loopback))
}

func restoreDNSReplySource(original, actual []byte) []byte {
	if isDNSStubSource(actual) {
		return original
	}
	return actual
}
