//go:build linux

package sandboxinit

import (
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type dnsRecvmsg struct {
	request     seccompNotif
	listenerFD  int
	inode       uint64
	fd          int
	original    []byte
	source      []byte
	message     targetMsghdr
	descriptors []byte
	size        int
	started     time.Time
	timeout     unix.Timeval
	nonblocking bool
}

// prepareDNSRecvmsg snapshots the receive layout, without consuming a packet.
// Non-DNS sockets retain the kernel path, including Unix descriptor passing.
func (s *egressSupervisor) prepareDNSRecvmsg(listenerFD int, request *seccompNotif) *dnsRecvmsg {
	if int(request.Data.Args[2])&unix.MSG_ERRQUEUE != 0 || !s.isDNSTargetSocket(int(request.PID), int(request.Data.Args[0])) {
		replyContinue(listenerFD, request.ID)
		return nil
	}
	fd, err := duplicateTargetFD(int(request.PID), int(request.Data.Args[0]))
	if err != nil {
		replyConnect(listenerFD, request.ID, errnoFromError(err))
		return nil
	}
	original, err := s.dnsSocketSource(fd)
	if err != nil {
		_ = unix.Close(fd)
		replyConnect(listenerFD, request.ID, errnoFromError(err))
		return nil
	}
	ok := original != nil
	if ok {
		socketType, socketErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
		ok = socketErr == nil && socketType == unix.SOCK_DGRAM
	}
	if !ok {
		_ = unix.Close(fd)
		replyContinue(listenerFD, request.ID)
		return nil
	}
	ownedListener, err := unix.FcntlInt(uintptr(listenerFD), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		_ = unix.Close(fd)
		replyConnect(listenerFD, request.ID, errnoFromError(err))
		return nil
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = unix.Close(fd)
		_ = unix.Close(ownedListener)
		replyConnect(listenerFD, request.ID, errnoFromError(err))
		return nil
	}
	receive := &dnsRecvmsg{request: *request, listenerFD: ownedListener, fd: fd, inode: stat.Ino, original: original}
	if errno := receive.readLayout(); errno != 0 {
		receive.close()
		replyConnect(listenerFD, request.ID, errno)
		return nil
	}
	return receive
}

func (r *dnsRecvmsg) readLayout() unix.Errno {
	message, err := readTargetMsghdr(int(r.request.PID), r.request.Data.Args[1])
	if err != nil {
		return unix.EFAULT
	}
	if message.Name != 0 && int32(message.Namelen) < 0 {
		return unix.EINVAL
	}
	// Linux UIO_MAXIOV; sendmsg's smaller policy limit is not a recvmsg ABI limit.
	if message.Iovlen > 1024 {
		return unix.EMSGSIZE
	}
	descriptors, err := readTargetMemoryBytes(int(r.request.PID), message.Iov, int(message.Iovlen)*16)
	if err != nil {
		return unix.EFAULT
	}
	var total uint64
	for offset := 0; offset < len(descriptors); offset += 16 {
		length := binary.NativeEndian.Uint64(descriptors[offset+8 : offset+16])
		if length > uint64(1<<63-1) {
			return unix.EINVAL
		}
		total += min(length, uint64(egressDatagramLimit)-total)
	}
	r.message, r.descriptors, r.size = message, descriptors, int(total)
	r.nonblocking = isNonblocking(r.fd) || int(r.request.Data.Args[2])&(unix.MSG_DONTWAIT|unix.MSG_ERRQUEUE) != 0
	if r.nonblocking {
		return 0
	}
	timeout, err := unix.GetsockoptTimeval(r.fd, unix.SOL_SOCKET, unix.SO_RCVTIMEO)
	if err != nil {
		return errnoFromError(err)
	}
	r.started, r.timeout = time.Now(), *timeout
	return 0
}

// Compare seconds separately: a valid SO_RCVTIMEO can exceed time.Duration.
func (r *dnsRecvmsg) expired() bool {
	if r.timeout.Sec == 0 && r.timeout.Usec == 0 {
		return false
	}
	elapsed := time.Since(r.started)
	seconds := int64(elapsed / time.Second)
	return seconds > r.timeout.Sec || seconds == r.timeout.Sec && int64(elapsed%time.Second/time.Microsecond) >= r.timeout.Usec
}

// isDNSTargetSocket uses the existing inode cache before the expensive pidfd
// duplication. Re-reading the fd symlink handles dup, fork and fd-number reuse;
// the duplicated descriptor is checked again before any emulation takes place.
func (s *egressSupervisor) isDNSTargetSocket(pid, fd int) bool {
	link, err := os.Readlink(fmt.Sprintf("/proc/%d/fd/%d", pid, fd))
	if err != nil {
		return false
	}
	inode, err := strconv.ParseUint(strings.TrimSuffix(strings.TrimPrefix(link, "socket:["), "]"), 10, 64)
	if err != nil {
		return false
	}
	s.dnsMu.Lock()
	_, ok := s.dnsSockets[inode]
	s.dnsMu.Unlock()
	return ok
}

func (r *dnsRecvmsg) close() {
	if r.fd >= 0 {
		_ = unix.Close(r.fd)
	}
	_ = unix.Close(r.listenerFD)
}

// try first peeks through the original layout to retain kernel validation.
// A complete consumed packet remains owned by the worker until NOTIF_SEND
// commits the result. If the notification disappears it is replayed on the
// next receive of the same socket, including across different exec listeners.
func (r *dnsRecvmsg) try(packet *dnsRecvmsgPacket) (bool, *dnsRecvmsgPacket) {
	if !notificationIDValid(r.listenerFD, r.request.ID) {
		return true, packet
	}
	buffer := make([]byte, r.size)
	var control []byte
	if r.message.Control != 0 {
		control = make([]byte, int(min(r.message.Controllen, uint64(egressDatagramLimit))))
	}
	n, controlSize, flags, err := r.receive(buffer, control, unix.MSG_PEEK)
	if packet != nil && errors.Is(err, unix.EAGAIN) {
		// The retained packet was consumed before this notification arrived;
		// UDP ignores MSG_OOB, so an empty kernel queue must still replay it.
		err = nil
	}
	if errors.Is(err, unix.EAGAIN) && !r.nonblocking && !r.expired() {
		return false, packet
	}
	if err != nil {
		replyConnect(r.listenerFD, r.request.ID, errnoFromError(err))
		return true, packet
	}
	if !notificationIDValid(r.listenerFD, r.request.ID) {
		return true, packet
	}
	peek := int(r.request.Data.Args[2])&unix.MSG_PEEK != 0
	if packet == nil && !peek {
		packet, err = readDNSRecvmsgPacket(r.fd, int(r.request.Data.Args[2]))
		if err != nil {
			if errors.Is(err, unix.EAGAIN) && !r.nonblocking && !r.expired() {
				return false, nil
			}
			replyConnect(r.listenerFD, r.request.ID, errnoFromError(err))
			return true, nil
		}
		// Transfer the duplicate to the packet so a failed response cannot allow
		// inode reuse or lose the data when this request is released.
		packet.fd = r.fd
		r.fd = -1
	}
	if packet != nil {
		buffer, control, n, flags = packet.message(r)
		r.source = packet.source
	} else {
		buffer, control = buffer[:min(n, len(buffer))], control[:controlSize]
	}
	return true, r.complete(packet, buffer, control, n, flags)
}

func (r *dnsRecvmsg) complete(packet *dnsRecvmsgPacket, payload, control []byte, n, flags int) *dnsRecvmsgPacket {
	if !notificationIDValid(r.listenerFD, r.request.ID) {
		return packet
	}
	response := seccompNotifResp{ID: r.request.ID, Val: int64(n)}
	if err := r.writeMessage(payload, control, flags); err != nil {
		response.Val = 0
		response.Error = -int32(unix.EFAULT)
	}
	if err := ioctlNotifSend(r.listenerFD, &response); err != nil {
		return packet
	}
	if int(r.request.Data.Args[2])&unix.MSG_PEEK == 0 && packet != nil {
		_ = unix.Close(packet.fd)
		return nil
	}
	return packet
}

func (r *dnsRecvmsg) writeMessage(payload, control []byte, flags int) error {
	pid, address := int(r.request.PID), r.request.Data.Args[1]
	message, descriptors := r.message, r.descriptors
	original := restoreDNSReplySource(r.original, r.source)
	for offset := 0; offset < len(descriptors) && len(payload) > 0; offset += 16 {
		base := binary.NativeEndian.Uint64(descriptors[offset : offset+8])
		length := int(min(binary.NativeEndian.Uint64(descriptors[offset+8:offset+16]), uint64(len(payload))))
		if err := writeTargetMemory(pid, base, payload[:length]); err != nil {
			return err
		}
		payload = payload[length:]
	}
	var nameLength [4]byte
	if message.Name != 0 {
		if err := writeTargetMemory(pid, message.Name, original[:min(int(message.Namelen), len(original))]); err != nil {
			return err
		}
		binary.NativeEndian.PutUint32(nameLength[:], uint32(len(original)))
		if err := writeTargetMemory(pid, address+8, nameLength[:]); err != nil {
			return err
		}
	}
	if err := writeTargetMemory(pid, message.Control, control); err != nil {
		return err
	}
	var controlLength [8]byte
	binary.NativeEndian.PutUint64(controlLength[:], uint64(len(control)))
	if err := writeTargetMemory(pid, address+40, controlLength[:]); err != nil {
		return err
	}
	var returnedFlags [4]byte
	binary.NativeEndian.PutUint32(returnedFlags[:], uint32(flags))
	return writeTargetMemory(pid, address+48, returnedFlags[:])
}

// Keep the original iovec lengths when asking the kernel to receive. Besides
// scatter placement, Linux validates the complete vector's address ranges even
// when the packet would fit into its first bytes. UDP bounds the copied payload.
func (r *dnsRecvmsg) receive(buffer, control []byte, extraFlags int) (int, int, int, error) {
	vectors := make([]unix.Iovec, len(r.descriptors)/16)
	offset := 0
	for index := range vectors {
		length := binary.NativeEndian.Uint64(r.descriptors[index*16+8 : index*16+16])
		if offset < len(buffer) {
			vectors[index].Base = &buffer[offset]
		}
		vectors[index].Len = length
		offset += int(min(length, uint64(len(buffer)-offset)))
	}
	var name unix.RawSockaddrAny
	message := unix.Msghdr{Name: (*byte)(unsafe.Pointer(&name)), Namelen: uint32(unsafe.Sizeof(name))}
	if len(vectors) > 0 {
		message.Iov = &vectors[0]
		message.Iovlen = uint64(len(vectors))
	}
	if len(control) > 0 {
		message.Control = &control[0]
		message.Controllen = uint64(len(control))
	}
	n, _, errno := unix.Syscall(unix.SYS_RECVMSG, uintptr(r.fd), uintptr(unsafe.Pointer(&message)), uintptr(int(r.request.Data.Args[2])|unix.MSG_DONTWAIT|extraFlags))
	if errno != 0 {
		return 0, 0, 0, errno
	}
	r.source = append([]byte(nil), unsafe.Slice((*byte)(unsafe.Pointer(&name)), min(int(message.Namelen), int(unsafe.Sizeof(name))))...)
	return int(n), int(message.Controllen), int(message.Flags), nil
}
