//go:build linux

package sandboxinit

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"runtime"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	// landlockCreateRulesetVersion is LANDLOCK_CREATE_RULESET_VERSION: asking
	// create_ruleset for the ABI version instead of creating a ruleset.
	landlockCreateRulesetVersion = 1 << 0
	// landlockRulePathBeneath is LANDLOCK_RULE_PATH_BENEATH.
	landlockRulePathBeneath = unix.LANDLOCK_RULE_PATH_BENEATH
)

// landlockFSFileAccess is the subset of access rights the kernel accepts on a
// non-directory rule. Directory-only rights are masked off before add_rule.
const landlockFSFileAccess = unix.LANDLOCK_ACCESS_FS_EXECUTE |
	unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
	unix.LANDLOCK_ACCESS_FS_READ_FILE |
	unix.LANDLOCK_ACCESS_FS_TRUNCATE |
	unix.LANDLOCK_ACCESS_FS_IOCTL_DEV

// landlockABI returns the kernel's Landlock ABI version. ENOSYS means the
// syscall does not exist; EOPNOTSUPP means the LSM is compiled in but disabled
// at boot. Both are reported as a probe failure so the configured fallback can
// decide between failing closed and an explicit warning.
func landlockABI() (int, error) {
	version, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, 0, 0, uintptr(landlockCreateRulesetVersion))
	if errno != 0 {
		return 0, errno
	}
	if version == 0 {
		return 0, errors.New("landlock_create_ruleset reported ABI 0")
	}
	return int(version), nil
}

// resolveLandlockSetup turns the configured mode and the kernel probe into the
// decision init acts on. It is pure so the unsupported-kernel policy can be
// tested without a kernel that lacks Landlock.
func resolveLandlockSetup(mode LandlockMode, fallback LandlockFallback, abi int, probeErr error) (bool, string, error) {
	if mode != LandlockModeEnforce {
		return false, "", nil
	}
	if probeErr != nil {
		if fallback == LandlockFallbackWarn {
			return false, fmt.Sprintf("the guest kernel cannot enforce Landlock: %v", probeErr), nil
		}
		return false, "", fmt.Errorf("sandbox init Landlock enforcement was requested but the guest kernel cannot enforce it: %w", probeErr)
	}
	if abi < 1 {
		err := fmt.Errorf("sandbox init Landlock enforcement was requested but the kernel reported ABI %d", abi)
		if fallback == LandlockFallbackWarn {
			return false, err.Error(), nil
		}
		return false, "", err
	}
	return true, "", nil
}

// landlockHandledFS is the deny-by-default access set. Rights a kernel does not
// know are rejected at ruleset creation, so they are gated by ABI. Network
// access rights are deliberately not handled: Landlock must not change the
// egress path, which is mediated separately by init's seccomp supervisor.
func landlockHandledFS(abi int) uint64 {
	handled := uint64(unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM)
	if abi >= 2 {
		handled |= unix.LANDLOCK_ACCESS_FS_REFER
	}
	if abi >= 3 {
		handled |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
	}
	return handled
}

// landlockScopeAccess expands a policy scope into kernel rights. REFER is
// always implicitly denied by a ruleset, so a read-write subtree must grant it
// explicitly or rename(2) inside the workspace fails.
func landlockScopeAccess(scope LandlockScope, abi int) (uint64, error) {
	read := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR)
	switch scope {
	case LandlockScopeRead:
		return read, nil
	case LandlockScopeReadExecute:
		return read | unix.LANDLOCK_ACCESS_FS_EXECUTE, nil
	case LandlockScopeReadWrite:
		access := read |
			unix.LANDLOCK_ACCESS_FS_EXECUTE |
			unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
			unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
			unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
			unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
			unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
			unix.LANDLOCK_ACCESS_FS_MAKE_REG |
			unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
			unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
			unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
			unix.LANDLOCK_ACCESS_FS_MAKE_SYM
		if abi >= 2 {
			access |= unix.LANDLOCK_ACCESS_FS_REFER
		}
		if abi >= 3 {
			access |= unix.LANDLOCK_ACCESS_FS_TRUNCATE
		}
		return access, nil
	default:
		return 0, fmt.Errorf("unknown sandbox init Landlock scope %q", scope)
	}
}

// applyLandlockPolicy installs the ruleset on the calling thread and enables
// no_new_privs. The caller must have locked the OS thread and must exec
// immediately afterward: Landlock restrictions are per-thread and inherited by
// the executed program.
func applyLandlockPolicy(policy LandlockPolicy) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	abi, err := landlockABI()
	if err != nil {
		return fmt.Errorf("probe Landlock ABI: %w", err)
	}
	rulesetFD, err := landlockCreateRuleset(landlockHandledFS(abi))
	if err != nil {
		return err
	}
	defer func() { _ = unix.Close(rulesetFD) }()
	for _, rule := range policy.Rules {
		if err := landlockAddPathRule(rulesetFD, rule, abi); err != nil {
			return err
		}
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs before restricting the filesystem: %w", err)
	}
	if _, _, errno := unix.Syscall(unix.SYS_LANDLOCK_RESTRICT_SELF, uintptr(rulesetFD), 0, 0); errno != 0 {
		return fmt.Errorf("landlock_restrict_self: %w", errno)
	}
	return nil
}

// landlockCreateRuleset passes only handled_access_fs. The kernel accepts a
// shorter ruleset attribute for older ABIs and treats the missing network field
// as zero, which is exactly what this baseline needs.
func landlockCreateRuleset(handledFS uint64) (int, error) {
	attr := struct {
		HandledAccessFS uint64
	}{HandledAccessFS: handledFS}
	fd, _, errno := unix.Syscall(unix.SYS_LANDLOCK_CREATE_RULESET, uintptr(unsafe.Pointer(&attr)), unsafe.Sizeof(attr), 0)
	runtime.KeepAlive(attr)
	if errno != 0 {
		return -1, fmt.Errorf("landlock_create_ruleset: %w", errno)
	}
	return int(fd), nil
}

// landlockAddPathRule opens one rule path and adds it to the ruleset. Missing
// read-only system paths (for example /lib32 on a 64-bit-only system) are
// skipped; a missing writable path is an error because silently dropping it
// would deny the workspace the sandbox was started with.
func landlockAddPathRule(rulesetFD int, rule LandlockRule, abi int) error {
	fd, err := unix.Open(rule.Path, unix.O_PATH|unix.O_CLOEXEC, 0)
	if err != nil {
		if isLandlockWritableScope(rule.Scope) {
			return fmt.Errorf("open sandbox init Landlock writable path %q: %w", rule.Path, err)
		}
		if errors.Is(err, unix.ENOENT) || errors.Is(err, unix.ENOTDIR) {
			return nil
		}
		return fmt.Errorf("open sandbox init Landlock read-only path %q: %w", rule.Path, err)
	}
	defer func() { _ = unix.Close(fd) }()
	access, err := landlockScopeAccess(rule.Scope, abi)
	if err != nil {
		return err
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return fmt.Errorf("stat sandbox init Landlock path %q: %w", rule.Path, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		access &= landlockFSFileAccess
	}
	// landlock_path_beneath_attr is packed: __u64 followed by __s32 (12 bytes).
	// landlock_add_rule takes (ruleset_fd, rule_type, rule_attr, flags); the
	// attribute size is implied by the rule type.
	var attr [12]byte
	binary.NativeEndian.PutUint64(attr[0:8], access)
	binary.NativeEndian.PutUint32(attr[8:12], uint32(fd))
	if _, _, errno := unix.Syscall6(unix.SYS_LANDLOCK_ADD_RULE, uintptr(rulesetFD), uintptr(landlockRulePathBeneath), uintptr(unsafe.Pointer(&attr[0])), 0, 0, 0); errno != 0 {
		return fmt.Errorf("landlock_add_rule for %q: %w", rule.Path, errno)
	}
	return nil
}

func isLandlockWritableScope(scope LandlockScope) bool {
	return scope == LandlockScopeReadWrite
}

// validateLandlockPolicyPaths checks the policy before init advertises it. It
// does not restrict init itself; it only proves that the trampoline will be
// able to open every rule path later.
func validateLandlockPolicyPaths(policy LandlockPolicy) error {
	for _, rule := range policy.Rules {
		fd, err := unix.Open(rule.Path, unix.O_PATH|unix.O_CLOEXEC, 0)
		if err != nil {
			if isLandlockWritableScope(rule.Scope) {
				return fmt.Errorf("sandbox init Landlock writable path %q is not available: %w", rule.Path, err)
			}
			continue
		}
		_ = unix.Close(fd)
	}
	return nil
}

// landlockSpawn is the per-exec configuration for the trampoline. The binary
// is the running init executable, re-executed with --exec-child so the ruleset
// is installed after fork and immediately before the workload's exec.
type landlockSpawn struct {
	binary     string
	policyJSON string
}

func (l *landlockSpawn) enabled() bool {
	return l != nil && l.policyJSON != ""
}

// prepareLandlockSpawn resolves the configured mode for this init instance.
// It returns a nil spawn when enforcement is off or explicitly degraded, and a
// non-empty warning when the configured fallback allowed degradation. Policy
// errors always fail: fallback is about kernel support, not about a malformed
// or unavailable workspace.
func prepareLandlockSpawn(options LandlockOptions) (*landlockSpawn, string, error) {
	mode, err := ParseLandlockMode(string(options.Mode))
	if err != nil {
		return nil, "", err
	}
	fallback, err := ParseLandlockFallback(string(options.Fallback))
	if err != nil {
		return nil, "", err
	}
	if mode != LandlockModeEnforce {
		return nil, "", nil
	}
	policy := options.Policy
	if policy == nil {
		built, err := DefaultLandlockPolicy(os.Environ())
		if err != nil {
			return nil, "", err
		}
		policy = &built
	}
	if err := policy.Validate(); err != nil {
		return nil, "", err
	}
	abi, probeErr := landlockABI()
	active, warning, err := resolveLandlockSetup(mode, fallback, abi, probeErr)
	if err != nil {
		return nil, "", err
	}
	if !active {
		return nil, warning, nil
	}
	if err := validateLandlockPolicyPaths(*policy); err != nil {
		return nil, "", err
	}
	binary, err := os.Executable()
	if err != nil {
		return nil, "", fmt.Errorf("resolve sandbox init binary for Landlock: %w", err)
	}
	payload, err := json.Marshal(policy)
	if err != nil {
		return nil, "", fmt.Errorf("encode sandbox init Landlock policy: %w", err)
	}
	return &landlockSpawn{binary: binary, policyJSON: string(payload)}, "", nil
}

// LandlockOptions configures the filesystem confinement init applies to every
// workload. Policy is only used by tests; production derives it from the guest
// environment so the daemon cannot forget to send it.
type LandlockOptions struct {
	Mode     LandlockMode
	Fallback LandlockFallback
	Policy   *LandlockPolicy
}
