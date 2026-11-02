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
	program := buildEgressFilter(arch, uint32(unix.SYS_CONNECT), uint32(unix.SYS_IO_URING_SETUP))
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
func buildEgressFilter(arch, connectNR, ioUringSetupNR uint32) []unix.SockFilter {
	return []unix.SockFilter{
		// 0: A = seccomp_data.arch
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: seccompDataArch},
		// 1: if arch matches continue at 2, otherwise go to 8 (kill)
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 6, K: arch},
		// 2: A = seccomp_data.nr
		{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: seccompDataNR},
		// 3: connect -> 4, otherwise 5
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: connectNR},
		// 4: SECCOMP_RET_USER_NOTIF
		{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_USER_NOTIF)},
		// 5: io_uring_setup -> 6, otherwise 7
		{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 0, Jf: 1, K: ioUringSetupNR},
		// 6: SECCOMP_RET_ERRNO(EPERM)
		{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_ERRNO) | uint32(unix.EPERM)},
		// 7: SECCOMP_RET_ALLOW
		{Code: unix.BPF_RET | unix.BPF_K, K: uint32(unix.SECCOMP_RET_ALLOW)},
		// 8: SECCOMP_RET_KILL_PROCESS
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

// sendListener sends the notification listener over the socketpair created by
// init. The trampoline must not exec the workload if this fails: a filter whose
// listener never reaches the supervisor answers connect with ENOSYS.
func sendListener(sock *os.File, listenerFD int) error {
	rights := unix.UnixRights(listenerFD)
	if err := unix.Sendmsg(int(sock.Fd()), []byte{1}, rights, nil, 0); err != nil {
		return fmt.Errorf("send sandbox init egress listener: %w", err)
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

// connectDestination decodes the sockaddr of an intercepted connect.
func connectDestination(pid int, addrPointer uint64, addrLen uint32) (EgressAuditEvent, error) {
	if addrLen == 0 || addrLen > seccompMaxSockaddr {
		return EgressAuditEvent{}, fmt.Errorf("connect address length %d is out of range", addrLen)
	}
	buf := make([]byte, addrLen)
	if err := readTargetMemory(pid, addrPointer, buf); err != nil {
		return EgressAuditEvent{}, err
	}
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
		return EgressAuditEvent{
			Family:   "unix",
			UnixPath: strings.TrimRight(string(buf[2:]), "\x00"),
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
