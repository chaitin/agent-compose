//go:build linux

package sandboxinit

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"os"
	"runtime"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// seccompDataNR and seccompDataArch are byte offsets in struct seccomp_data.
	seccompDataNR   = 0
	seccompDataArch = 4
	// seccompDataArgs is the offset of the args[6] array in struct seccomp_data.
	seccompDataArgs = 16
	// seccompUserNotifContinue tells the kernel to execute the intercepted
	// syscall after the supervisor inspected its arguments.
	seccompUserNotifContinue = 0x00000001
	// seccompMaxSockaddr bounds the sockaddr length the supervisor is willing
	// to read from the target. Unix domain addresses are the largest at 110.
	seccompMaxSockaddr = 128
)

// seccompData mirrors struct seccomp_data.
type seccompData struct {
	NR                 int32
	Arch               uint32
	InstructionPointer uint64
	Args               [6]uint64
}

// seccompNotif mirrors struct seccomp_notif.
type seccompNotif struct {
	ID    uint64
	PID   uint32
	Flags uint32
	Data  seccompData
}

// seccompNotifResp mirrors struct seccomp_notif_resp.
type seccompNotifResp struct {
	ID    uint64
	Val   int64
	Error int32
	Flags uint32
}

// seccompNotifSizes mirrors struct seccomp_notif_sizes.
type seccompNotifSizes struct {
	Notif     uint16
	NotifResp uint16
	Data      uint16
}

// seccompFilterArch returns the AUDIT_ARCH value of the running kernel ABI.
func seccompFilterArch() (uint32, error) {
	switch runtime.GOARCH {
	case "amd64":
		return unix.AUDIT_ARCH_X86_64, nil
	case "arm64":
		return unix.AUDIT_ARCH_AARCH64, nil
	default:
		return 0, fmt.Errorf("sandbox init egress interception does not support %s", runtime.GOARCH)
	}
}

// egressInterceptionSupported asks the kernel two questions: does it know the
// user-notification action, and does the control protocol support continuing
// an intercepted syscall (Linux 5.5). Audit mode needs both; a mode that cannot
// be delivered is disabled and reported, never silently claimed.
func egressInterceptionSupported() error {
	if _, err := seccompFilterArch(); err != nil {
		return err
	}
	release, err := kernelRelease()
	if err != nil {
		return fmt.Errorf("read kernel release for egress interception: %w", err)
	}
	major, minor, err := parseKernelRelease(release)
	if err != nil {
		return err
	}
	if major < 5 || (major == 5 && minor < 5) {
		return fmt.Errorf("kernel %s predates seccomp user notification continue (5.5)", release)
	}
	action := uint32(unix.SECCOMP_RET_USER_NOTIF)
	if _, err := seccompSyscall(unix.SECCOMP_GET_ACTION_AVAIL, 0, unsafe.Pointer(&action)); err != nil {
		return fmt.Errorf("kernel does not support SECCOMP_RET_USER_NOTIF: %w", err)
	}
	return nil
}

func kernelRelease() (string, error) {
	var uts unix.Utsname
	if err := unix.Uname(&uts); err != nil {
		return "", err
	}
	return strings.TrimRight(string(uts.Release[:]), "\x00"), nil
}

func parseKernelRelease(release string) (int, int, error) {
	parts := strings.SplitN(release, ".", 3)
	if len(parts) < 2 {
		return 0, 0, fmt.Errorf("kernel release %q is not major.minor", release)
	}
	var major, minor int
	if _, err := fmt.Sscanf(parts[0], "%d", &major); err != nil {
		return 0, 0, fmt.Errorf("kernel release %q has no major version: %w", release, err)
	}
	minorPart := parts[1]
	for i, r := range minorPart {
		if r < '0' || r > '9' {
			minorPart = minorPart[:i]
			break
		}
	}
	if _, err := fmt.Sscanf(minorPart, "%d", &minor); err != nil {
		return 0, 0, fmt.Errorf("kernel release %q has no minor version: %w", release, err)
	}
	return major, minor, nil
}

// installEgressFilter installs a filter that hands connect to the supervisor
// and refuses io_uring_setup, the known path around a seccomp-only egress
// interceptor. It returns the user-notification listener.
//
// The caller must set no_new_privs first and must run on the thread that is
// about to exec the workload: seccomp filters are per-thread and inherited
// across exec.
func installEgressFilter() (int, error) {
	arch, err := seccompFilterArch()
	if err != nil {
		return -1, err
	}
	program := buildEgressFilter(arch, egressFilterSyscalls{
		Connect:      uint32(unix.SYS_CONNECT),
		IOUringSetup: uint32(unix.SYS_IO_URING_SETUP),
		Sendto:       uint32(unix.SYS_SENDTO),
		Sendmsg:      uint32(unix.SYS_SENDMSG),
		Recvfrom:     uint32(unix.SYS_RECVFROM),
		Getpeername:  uint32(unix.SYS_GETPEERNAME),
		Recvmsg:      uint32(unix.SYS_RECVMSG),
	})
	fprog := unix.SockFprog{
		Len:    uint16(len(program)),
		Filter: &program[0],
	}
	fd, err := seccompSyscall(unix.SECCOMP_SET_MODE_FILTER, unix.SECCOMP_FILTER_FLAG_NEW_LISTENER, unsafe.Pointer(&fprog))
	if err != nil {
		return -1, fmt.Errorf("install sandbox init egress filter: %w", err)
	}
	return int(fd), nil
}

// buildEgressFilter returns the classic BPF program. connect is reported to
// user space; io_uring_setup fails closed; an architecture mismatch kills the
// process rather than letting a foreign ABI slip past the connect check.
// buildEgressFilter returns the classic BPF program:
//
//	connect          -> user notification
//	io_uring_setup   -> EPERM (io_uring bypasses seccomp connect checks)
//	sendmsg          -> user notification (the name is only visible in memory)
//	sendto with a destination pointer -> user notification
//	everything else  -> allow
//
// A connected sendto and a sendmsg without a name are left to the kernel: the
// socket was already redirected to the policy DNS when it connected, and
// notifying every stream write would add an upcall per write.
// hardeningSyscalls are refused with EPERM by the second, listener-less filter
// installed in enforce mode. The list is namespace, kernel, tracing, and
// keyring primitives a sandboxed agent does not need and could use to escape
// the mediated data plane.
//
// sendmmsg/recvmmsg are deliberately not in this list. The trampoline hands the
// notification listener to init with sendmmsg, the only SCM_RIGHTS path the
// filter does not notify; notifying sendmmsg would deadlock that handoff, and a
// hard deny breaks resolvers (glibc's getaddrinfo uses it in the test
// environment). The residual is arbitrary UDP egress, not only the policy
// resolver, and the driver's network policy report discloses it. Closing it
// needs a different listener handoff, not another deny entry.
func hardeningSyscalls() []uint32 {
	return []uint32{
		uint32(unix.SYS_UNSHARE),
		uint32(unix.SYS_SETNS),
		uint32(unix.SYS_MOUNT),
		uint32(unix.SYS_UMOUNT2),
		uint32(unix.SYS_PIVOT_ROOT),
		uint32(unix.SYS_BPF),
		uint32(unix.SYS_PERF_EVENT_OPEN),
		uint32(unix.SYS_KEXEC_LOAD),
		uint32(unix.SYS_KEXEC_FILE_LOAD),
		uint32(unix.SYS_USERFAULTFD),
		uint32(unix.SYS_PTRACE),
		uint32(unix.SYS_INIT_MODULE),
		uint32(unix.SYS_FINIT_MODULE),
		uint32(unix.SYS_DELETE_MODULE),
		uint32(unix.SYS_OPEN_BY_HANDLE_AT),
		uint32(unix.SYS_NAME_TO_HANDLE_AT),
		uint32(unix.SYS_PROCESS_VM_READV),
		uint32(unix.SYS_PROCESS_VM_WRITEV),
		uint32(unix.SYS_PIDFD_GETFD),
	}
}

// buildHardeningFilter returns a listener-less filter that refuses the listed
// syscalls. It is installed after the notification listener has been handed to
// init, so sendmmsg can be denied without blocking the trampoline's own
// handoff. Filters only stack, so an agent cannot remove this later.
func buildHardeningFilter(arch uint32, syscalls []uint32) []unix.SockFilter {
	program := make([]unix.SockFilter, 0, 5+len(syscalls)*2)
	program = append(program,
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: seccompDataArch},
	)
	archCheck := len(program)
	program = append(program, unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, K: arch})
	program = append(program,
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: seccompDataNR},
	)
	for _, syscall := range syscalls {
		program = append(program,
			unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: syscall},
			unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EPERM)},
		)
	}
	program = append(program, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_ALLOW)})
	killIndex := len(program)
	program = append(program, unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_KILL_PROCESS)})
	program[archCheck].Jt = 0
	program[archCheck].Jf = uint8(killIndex - (archCheck + 1))
	return program
}

// installHardeningFilter installs the deny list without a listener. The caller
// must have no_new_privs set and must still hold CAP_SETPCAP/the ability to
// install filters; a failure is fatal in enforce mode.
func installHardeningFilter() error {
	arch, err := seccompFilterArch()
	if err != nil {
		return err
	}
	program := buildHardeningFilter(arch, hardeningSyscalls())
	fprog := unix.SockFprog{Len: uint16(len(program)), Filter: &program[0]}
	if _, err := seccompSyscall(unix.SECCOMP_SET_MODE_FILTER, 0, unsafe.Pointer(&fprog)); err != nil {
		return fmt.Errorf("install sandbox init hardening filter: %w", err)
	}
	return nil
}

// egressFilterSyscalls names the syscalls the filter treats specially.
type egressFilterSyscalls struct {
	Connect      uint32
	IOUringSetup uint32
	Sendto       uint32
	Sendmsg      uint32
	Recvfrom     uint32
	Getpeername  uint32
	Recvmsg      uint32
}

func buildEgressFilter(arch uint32, syscalls egressFilterSyscalls) []unix.SockFilter {
	return []unix.SockFilter{
		// 0: A = seccomp_data.arch
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: seccompDataArch},
		// 1: if arch matches continue at 2, otherwise kill at 22
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 20, K: arch},
		// 2: A = seccomp_data.nr
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: seccompDataNR},
		// 3: connect -> 4, otherwise 5
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: syscalls.Connect},
		// 4: SECCOMP_RET_USER_NOTIF
		{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_USER_NOTIF)},
		// 5: io_uring_setup -> 6, otherwise 7
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: syscalls.IOUringSetup},
		// 6: SECCOMP_RET_ERRNO(EPERM)
		{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EPERM)},
		// 7: sendmsg -> 8 (always notify), otherwise 9
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: syscalls.Sendmsg},
		// 8: SECCOMP_RET_USER_NOTIF
		{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_USER_NOTIF)},
		// 9: recvfrom -> 10 (the supervisor continues non-DNS sockets), otherwise 11
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: syscalls.Recvfrom},
		// 10: SECCOMP_RET_USER_NOTIF
		{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_USER_NOTIF)},
		// 11: getpeername -> 12 (source spoofing for redirected DNS sockets), otherwise 13
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: syscalls.Getpeername},
		// 12: SECCOMP_RET_USER_NOTIF
		{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_USER_NOTIF)},
		// 13: recvmsg -> 14 (temporary observation of resolver reads), otherwise 15
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: syscalls.Recvmsg},
		// 14: SECCOMP_RET_USER_NOTIF
		{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_USER_NOTIF)},
		// 15: sendto -> 16, otherwise allow at 21
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 5, K: syscalls.Sendto},
		// 16: A = low word of args[4] (the destination pointer)
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: seccompDataArgs + 4*8},
		// 17: a zero low word may still be a non-null pointer, so continue to the high word
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 2, K: 0},
		// 18: A = high word of args[4]
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: seccompDataArgs + 4*8 + 4},
		// 19: both words are zero -> allow at 21, otherwise notify at 20
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, Jf: 0, K: 0},
		// 20: SECCOMP_RET_USER_NOTIF
		{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_USER_NOTIF)},
		// 21: SECCOMP_RET_ALLOW
		{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_ALLOW)},
		// 22: SECCOMP_RET_KILL_PROCESS
		{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_KILL_PROCESS)},
	}
}

func seccompSyscall(operation uintptr, flags uintptr, arg unsafe.Pointer) (uintptr, error) {
	var argPointer uintptr
	if arg != nil {
		argPointer = uintptr(arg)
	}
	result, _, errno := unix.Syscall(unix.SYS_SECCOMP, operation, flags, argPointer)
	if errno != 0 {
		return 0, errno
	}
	return result, nil
}

func notificationSizes() (seccompNotifSizes, error) {
	var sizes seccompNotifSizes
	if _, err := seccompSyscall(unix.SECCOMP_GET_NOTIF_SIZES, 0, unsafe.Pointer(&sizes)); err != nil {
		return seccompNotifSizes{}, fmt.Errorf("query seccomp notification sizes: %w", err)
	}
	if int(sizes.Notif) < int(unsafe.Sizeof(seccompNotif{})) || int(sizes.NotifResp) < int(unsafe.Sizeof(seccompNotifResp{})) || int(sizes.Data) < int(unsafe.Sizeof(seccompData{})) {
		return seccompNotifSizes{}, fmt.Errorf("kernel seccomp notification structures are smaller than expected")
	}
	return sizes, nil
}

func ioctlNotifRecv(listenerFD int, request *seccompNotif) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(listenerFD), uintptr(unix.SECCOMP_IOCTL_NOTIF_RECV), uintptr(unsafe.Pointer(request)))
	if errno != 0 {
		return errno
	}
	return nil
}

func ioctlNotifSend(listenerFD int, response *seccompNotifResp) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(listenerFD), uintptr(unix.SECCOMP_IOCTL_NOTIF_SEND), uintptr(unsafe.Pointer(response)))
	if errno != 0 {
		return errno
	}
	return nil
}

func notificationIDValid(listenerFD int, id uint64) bool {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, uintptr(listenerFD), uintptr(unix.SECCOMP_IOCTL_NOTIF_ID_VALID), uintptr(unsafe.Pointer(&id)))
	return errno == 0
}

// rawMsghdr and rawMmsghdr mirror the kernel's sendmmsg ABI. The listener is
// handed over with sendmmsg rather than sendmsg because the installed filter
// notifies sendmsg: using sendmsg would make the trampoline wait for a
// supervisor that cannot exist until the listener arrives.
type rawMsghdr struct {
	Name       uint64
	Namelen    uint32
	_          uint32
	Iov        uint64
	Iovlen     uint64
	Control    uint64
	Controllen uint64
	Flags      int32
	_          int32
}

type rawMmsghdr struct {
	Msg    rawMsghdr
	Msglen uint32
	_      uint32
}

// sendListener sends the notification listener over the socketpair created by
// init. The trampoline must not exec the workload if this fails: a filter whose
// listener never reaches the supervisor answers connect with ENOSYS.
func sendListener(sock *os.File, listenerFD int) error {
	payload := []byte{1}
	control := make([]byte, unix.CmsgSpace(4))
	binary.NativeEndian.PutUint64(control[0:8], uint64(unix.CmsgLen(4)))
	binary.NativeEndian.PutUint32(control[8:12], uint32(unix.SOL_SOCKET))
	binary.NativeEndian.PutUint32(control[12:16], uint32(unix.SCM_RIGHTS))
	binary.NativeEndian.PutUint32(control[16:20], uint32(listenerFD))
	iov := []unix.Iovec{{Base: &payload[0], Len: 1}}
	messages := []rawMmsghdr{{
		Msg: rawMsghdr{
			Iov:        uint64(uintptr(unsafe.Pointer(&iov[0]))),
			Iovlen:     1,
			Control:    uint64(uintptr(unsafe.Pointer(&control[0]))),
			Controllen: uint64(len(control)),
		},
	}}
	sent, _, errno := unix.Syscall6(unix.SYS_SENDMMSG, sock.Fd(), uintptr(unsafe.Pointer(&messages[0])), 1, 0, 0, 0)
	if errno != 0 {
		return fmt.Errorf("send sandbox init egress listener: %w", errno)
	}
	if sent != 1 {
		return fmt.Errorf("send sandbox init egress listener: sent %d messages, want 1", sent)
	}
	return nil
}

// receiveListener receives the trampoline's notification listener.
func receiveListener(sock *os.File) (int, error) {
	data := make([]byte, 1)
	oob := make([]byte, unix.CmsgSpace(4))
	_, oobn, _, _, err := unix.Recvmsg(int(sock.Fd()), data, oob, 0)
	if err != nil {
		return -1, fmt.Errorf("receive sandbox init egress listener: %w", err)
	}
	if oobn == 0 {
		return -1, errors.New("sandbox init trampoline exited without an egress listener")
	}
	messages, err := unix.ParseSocketControlMessage(oob[:oobn])
	if err != nil {
		return -1, fmt.Errorf("parse sandbox init egress listener message: %w", err)
	}
	if len(messages) != 1 {
		return -1, fmt.Errorf("sandbox init egress listener message carried %d control blocks, want 1", len(messages))
	}
	fds, err := unix.ParseUnixRights(&messages[0])
	if err != nil {
		return -1, fmt.Errorf("parse sandbox init egress listener rights: %w", err)
	}
	if len(fds) != 1 {
		return -1, fmt.Errorf("sandbox init egress listener message carried %d descriptors, want 1", len(fds))
	}
	unix.CloseOnExec(fds[0])
	return fds[0], nil
}

// readConnectAddress copies the sockaddr of an intercepted connect out of the
// target process. The raw bytes are kept because mediation re-connects the
// duplicated socket with the exact address the workload supplied.
func readConnectAddress(pid int, addrPointer uint64, addrLen uint32) ([]byte, error) {
	if addrLen == 0 || addrLen > seccompMaxSockaddr {
		return nil, fmt.Errorf("connect address length %d is out of range", addrLen)
	}
	buf := make([]byte, addrLen)
	if err := readTargetMemory(pid, addrPointer, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// connectDestination decodes the sockaddr of an intercepted connect.
func connectDestination(pid int, addrPointer uint64, addrLen uint32) (EgressAuditEvent, error) {
	buf, err := readConnectAddress(pid, addrPointer, addrLen)
	if err != nil {
		return EgressAuditEvent{}, err
	}
	return decodeConnectAddress(buf)
}

// decodeConnectAddress parses a raw sockaddr.
func decodeConnectAddress(buf []byte) (EgressAuditEvent, error) {
	if len(buf) < 2 {
		return EgressAuditEvent{}, errors.New("connect address is shorter than a family field")
	}
	family := binary.NativeEndian.Uint16(buf[:2])
	switch family {
	case unix.AF_INET:
		if len(buf) < 8 {
			return EgressAuditEvent{}, errors.New("short IPv4 sockaddr")
		}
		return EgressAuditEvent{
			Family: "inet",
			Host:   net.IP(buf[4:8]).String(),
			Port:   int(binary.BigEndian.Uint16(buf[2:4])),
		}, nil
	case unix.AF_INET6:
		if len(buf) < 24 {
			return EgressAuditEvent{}, errors.New("short IPv6 sockaddr")
		}
		return EgressAuditEvent{
			Family: "inet6",
			Host:   net.IP(buf[8:24]).String(),
			Port:   int(binary.BigEndian.Uint16(buf[2:4])),
		}, nil
	case unix.AF_UNIX:
		path, _, _ := strings.Cut(string(buf[2:]), "\x00")
		return EgressAuditEvent{
			Family:   "unix",
			UnixPath: path,
		}, nil
	default:
		return EgressAuditEvent{Family: fmt.Sprintf("family-%d", family)}, nil
	}
}

func readTargetMemory(pid int, address uint64, buf []byte) error {
	if len(buf) == 0 {
		return nil
	}
	local := []unix.Iovec{{Base: &buf[0], Len: uint64(len(buf))}}
	remote := []unix.RemoteIovec{{Base: uintptr(address), Len: len(buf)}}
	n, err := unix.ProcessVMReadv(pid, local, remote, 0)
	if err != nil {
		return fmt.Errorf("read target sockaddr: %w", err)
	}
	if n != len(buf) {
		return fmt.Errorf("read target sockaddr: read %d of %d bytes", n, len(buf))
	}
	return nil
}
