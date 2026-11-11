//go:build linux

package sandboxinit

import (
	"encoding/binary"
	"time"

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
	payload, control := make([]byte, egressDatagramLimit), make([]byte, egressDatagramLimit)
	n, oobn, returned, _, err := unix.Recvmsg(fd, payload, control, flags|unix.MSG_DONTWAIT)
	if err != nil {
		return nil, err
	}
	return &dnsRecvmsgPacket{payload: payload[:min(n, len(payload))], control: control[:oobn], flags: returned, expires: time.Now().Add(dnsSocketLifetime)}, nil
}

func (p *dnsRecvmsgPacket) message(r *dnsRecvmsg) ([]byte, []byte, int, int) {
	flags := p.flags &^ (unix.MSG_TRUNC | unix.MSG_CTRUNC | unix.MSG_CMSG_CLOEXEC)
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
