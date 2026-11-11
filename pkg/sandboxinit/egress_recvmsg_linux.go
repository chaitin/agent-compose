//go:build linux

package sandboxinit

import (
	"encoding/binary"
	"errors"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

type dnsRecvmsg struct {
	request     seccompNotif
	fd          int
	original    []byte
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
	fd, err := duplicateTargetFD(int(request.PID), int(request.Data.Args[0]))
	if err != nil {
		replyContinue(listenerFD, request.ID)
		return nil
	}
	original, ok := s.dnsSocketSource(fd)
	if ok {
		socketType, socketErr := unix.GetsockoptInt(fd, unix.SOL_SOCKET, unix.SO_TYPE)
		ok = socketErr == nil && socketType == unix.SOCK_DGRAM
	}
	if !ok {
		_ = unix.Close(fd)
		replyContinue(listenerFD, request.ID)
		return nil
	}
	receive := &dnsRecvmsg{request: *request, fd: fd, original: original}
	if errno := receive.readLayout(); errno != 0 {
		_ = unix.Close(fd)
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

// try receives only while the notification is live. It never blocks the
// notification reader, so another thread can send the packet being awaited.
func (r *dnsRecvmsg) try(listenerFD int) bool {
	if !notificationIDValid(listenerFD, r.request.ID) {
		return true
	}
	flagsIn := int(r.request.Data.Args[2])
	nonblocking := r.nonblocking
	// Avoid allocating payload buffers while a blocking receive is still idle.
	polls := []unix.PollFd{{Fd: int32(r.fd), Events: unix.POLLIN}}
	if flagsIn&unix.MSG_ERRQUEUE != 0 {
		polls[0].Events = unix.POLLERR
	}
	ready, err := unix.Poll(polls, 0)
	if err != nil {
		replyConnect(listenerFD, r.request.ID, errnoFromError(err))
		return true
	}
	if ready == 0 && !nonblocking {
		if !r.expired() {
			return false
		}
		replyConnect(listenerFD, r.request.ID, unix.EAGAIN)
		return true
	}
	buffer := make([]byte, r.size)
	var control []byte
	if r.message.Control != 0 {
		control = make([]byte, int(min(r.message.Controllen, uint64(egressDatagramLimit))))
	}
	n, controlSize, flags, err := r.receive(buffer, control)
	if errors.Is(err, unix.EAGAIN) && !nonblocking {
		if !r.expired() {
			return false
		}
	}
	if err != nil {
		replyConnect(listenerFD, r.request.ID, errnoFromError(err))
		return true
	}
	if !notificationIDValid(listenerFD, r.request.ID) {
		return true
	}
	if err := r.writeMessage(buffer[:min(n, len(buffer))], control[:controlSize], flags); err != nil {
		replyConnect(listenerFD, r.request.ID, unix.EFAULT)
		return true
	}
	_ = ioctlNotifSend(listenerFD, &seccompNotifResp{ID: r.request.ID, Val: int64(n)})
	return true
}

func (r *dnsRecvmsg) writeMessage(payload, control []byte, flags int) error {
	pid, address := int(r.request.PID), r.request.Data.Args[1]
	message, descriptors, original := r.message, r.descriptors, r.original
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
func (r *dnsRecvmsg) receive(buffer, control []byte) (int, int, int, error) {
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
	var message unix.Msghdr
	if len(vectors) > 0 {
		message.Iov = &vectors[0]
		message.Iovlen = uint64(len(vectors))
	}
	if len(control) > 0 {
		message.Control = &control[0]
		message.Controllen = uint64(len(control))
	}
	n, _, errno := unix.Syscall(unix.SYS_RECVMSG, uintptr(r.fd), uintptr(unsafe.Pointer(&message)), uintptr(int(r.request.Data.Args[2])|unix.MSG_DONTWAIT))
	if errno != 0 {
		return 0, 0, 0, errno
	}
	return int(n), int(message.Controllen), int(message.Flags), nil
}
