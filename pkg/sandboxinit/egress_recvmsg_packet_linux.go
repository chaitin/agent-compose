//go:build linux

package sandboxinit

import (
	"bytes"
	"encoding/binary"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

// The held fd prevents inode reuse while a canceled receive's packet is saved.
// Only the single supervisor receive worker accesses these packets.
type dnsRecvmsgPacket struct {
	fd               int
	payload, control []byte
	flags            int
	expires          time.Time
}

func readDNSRecvmsgPacket(fd, flags int) (*dnsRecvmsgPacket, error) {
	// Probe with MSG_TRUNC to learn the full datagram length even if the
	// caller's buffer is short. Grow ancillary space only when the kernel says
	// it is needed; ordinary DNS sockets usually have none.
	control := make([]byte, unix.CmsgSpace(int(unsafe.Sizeof(unix.Timeval{}))))
	var size int
	for {
		n, _, returned, _, err := unix.Recvmsg(fd, nil, control, flags|unix.MSG_PEEK|unix.MSG_TRUNC|unix.MSG_DONTWAIT)
		if err != nil {
			return nil, err
		}
		size = n
		if returned&unix.MSG_CTRUNC == 0 || len(control) >= egressDatagramLimit {
			break
		}
		control = make([]byte, min(len(control)*2, egressDatagramLimit))
	}
	payload := make([]byte, min(size, egressDatagramLimit))
	n, oobn, returned, _, err := unix.Recvmsg(fd, payload, control, flags|unix.MSG_DONTWAIT)
	if err != nil {
		return nil, err
	}
	if oobn == 0 {
		control = nil
	} else {
		control = bytes.Clone(control[:oobn])
	}
	return &dnsRecvmsgPacket{payload: payload[:min(n, len(payload))], control: control, flags: returned, expires: time.Now().Add(dnsSocketLifetime)}, nil
}

func (p *dnsRecvmsgPacket) message(r *dnsRecvmsg) ([]byte, []byte, int, int) {
	flags := p.flags &^ (unix.MSG_TRUNC | unix.MSG_CMSG_CLOEXEC)
	flags |= int(r.request.Data.Args[2]) & unix.MSG_CMSG_CLOEXEC
	n := min(r.size, len(p.payload))
	payload := p.payload[:n]
	if n < len(p.payload) {
		flags |= unix.MSG_TRUNC
		if int(r.request.Data.Args[2])&unix.MSG_TRUNC != 0 {
			n = len(p.payload)
		}
	}
	capacity := 0
	if r.message.Control != 0 {
		capacity = int(min(r.message.Controllen, uint64(len(p.control))))
	}
	control := make([]byte, 0, capacity)
	for offset := 0; offset < len(p.control); {
		length := int(binary.NativeEndian.Uint64(p.control[offset:]))
		available := capacity - len(control)
		if available < unix.CmsgLen(0) {
			flags |= unix.MSG_CTRUNC
			break
		}
		copied := min(length, available)
		start := len(control)
		size := min(unix.CmsgSpace(length-unix.CmsgLen(0)), available)
		control = append(control, p.control[offset:offset+size]...)
		binary.NativeEndian.PutUint64(control[start:], uint64(copied))
		if copied < length {
			flags |= unix.MSG_CTRUNC
			break
		}
		offset += unix.CmsgSpace(length - unix.CmsgLen(0))
	}
	return payload, control, n, flags
}

// Only canceled replies remain in this cache after receive commits. Under
// pressure evict the oldest abandoned reply at insertion, never refuse a live
// receive merely because other sockets have filled the recovery cache.
type dnsRecvmsgPackets map[uint64]*dnsRecvmsgPacket

func (saved dnsRecvmsgPackets) receive(r *dnsRecvmsg) bool {
	done, packet := r.try(saved[r.inode])
	if packet == nil {
		delete(saved, r.inode)
		return done
	}
	saved.retain(r.inode, packet)
	return done
}

func (saved dnsRecvmsgPackets) retain(inode uint64, packet *dnsRecvmsgPacket) {
	if saved[inode] == nil && len(saved) >= dnsRecvmsgPendingLimit {
		var oldest uint64
		var expiry time.Time
		for inode, p := range saved {
			if expiry.IsZero() || p.expires.Before(expiry) {
				oldest, expiry = inode, p.expires
			}
		}
		_ = unix.Close(saved[oldest].fd)
		delete(saved, oldest)
	}
	saved[inode] = packet
}
