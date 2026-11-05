package sandboxinit

import (
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// LandlockMode selects whether the init process confines the filesystem view
// of every workload it starts.
//
// The zero value and off are indistinguishable: an init built without Landlock
// support, or started without an explicit mode, behaves as before. Enforce
// makes a guest kernel without Landlock a startup decision instead of a silent
// downgrade; see LandlockFallback.
type LandlockMode string

const (
	// LandlockModeOff leaves workload filesystem access unrestricted.
	LandlockModeOff LandlockMode = "off"
	// LandlockModeEnforce applies a Landlock ruleset to every started workload.
	LandlockModeEnforce LandlockMode = "enforce"
)

// LandlockFallback decides what init does when enforce is requested but the
// guest kernel cannot provide Landlock (ENOSYS, EOPNOTSUPP, or a disabled LSM).
//
// Fail is the default: asking for enforcement must not silently produce a
// sandbox that reports confinement it does not have. Warn is the explicit
// escape hatch for a deployment that prefers availability; init then logs the
// missing mechanism and does not advertise the capability.
type LandlockFallback string

const (
	// LandlockFallbackFail refuses to start workloads unconfined.
	LandlockFallbackFail LandlockFallback = "fail"
	// LandlockFallbackWarn logs the unavailable mechanism and runs unconfined.
	LandlockFallbackWarn LandlockFallback = "warn"
)

const (
	// LandlockModeEnvVar carries the requested mode from the daemon into the
	// sandbox. It is an environment variable rather than a flag so a newer
	// daemon never makes an older guest init reject its entrypoint arguments.
	LandlockModeEnvVar = "SANDBOX_INIT_LANDLOCK"
	// LandlockFallbackEnvVar carries the unsupported-kernel policy. It only
	// matters when LandlockModeEnvVar is enforce.
	LandlockFallbackEnvVar = "SANDBOX_INIT_LANDLOCK_FALLBACK"
	// landlockPolicyEnvVar carries the resolved ruleset from init to the exec
	// trampoline. Init owns this value; an exec request cannot override it.
	landlockPolicyEnvVar = "SANDBOX_INIT_LANDLOCK_POLICY"
)

// ParseLandlockMode validates the configured mode.
func ParseLandlockMode(raw string) (LandlockMode, error) {
	switch LandlockMode(strings.ToLower(strings.TrimSpace(raw))) {
	case "", LandlockModeOff:
		return LandlockModeOff, nil
	case LandlockModeEnforce:
		return LandlockModeEnforce, nil
	default:
		return "", fmt.Errorf("%s must be off or enforce, got %q", LandlockModeEnvVar, raw)
	}
}

// ParseLandlockFallback validates the unsupported-kernel policy. An empty value
// selects fail, because enforce must mean enforce unless an operator explicitly
// chooses the availability tradeoff.
func ParseLandlockFallback(raw string) (LandlockFallback, error) {
	switch LandlockFallback(strings.ToLower(strings.TrimSpace(raw))) {
	case "", LandlockFallbackFail:
		return LandlockFallbackFail, nil
	case LandlockFallbackWarn:
		return LandlockFallbackWarn, nil
	default:
		return "", fmt.Errorf("%s must be fail or warn, got %q", LandlockFallbackEnvVar, raw)
	}
}

// LandlockScope names the access a rule grants. It is deliberately coarser than
// the kernel bitmask: the policy is a baseline, not a per-path capability
// language, and keeping the meaning small makes the policy auditable.
type LandlockScope string

const (
	// LandlockScopeRead allows reading files and listing directories.
	LandlockScopeRead LandlockScope = "read"
	// LandlockScopeReadExecute additionally allows executing files.
	LandlockScopeReadExecute LandlockScope = "read-execute"
	// LandlockScopeReadWrite allows creating, removing, renaming, truncating,
	// and executing files in the subtree.
	LandlockScopeReadWrite LandlockScope = "read-write"
)

// LandlockRule grants one scope to a path hierarchy.
type LandlockRule struct {
	Path  string        `json:"path"`
	Scope LandlockScope `json:"scope"`
}

// LandlockPolicy is the ruleset init sends to each exec trampoline. Everything
// outside its rules is denied, including the init binary directory and the
// control socket directory.
type LandlockPolicy struct {
	Rules []LandlockRule `json:"rules"`
}

// Validate rejects a policy the trampoline could not apply deterministically.
// Absolute, already-clean paths keep the JSON representation and the syscall
// path identical, so the daemon and init cannot disagree about the path.
func (p LandlockPolicy) Validate() error {
	if len(p.Rules) == 0 {
		return errors.New("sandbox init Landlock policy has no rules")
	}
	seen := make(map[LandlockRule]struct{}, len(p.Rules))
	for _, rule := range p.Rules {
		if strings.TrimSpace(rule.Path) == "" {
			return errors.New("sandbox init Landlock rule has an empty path")
		}
		if !filepath.IsAbs(rule.Path) {
			return fmt.Errorf("sandbox init Landlock path must be absolute, got %q", rule.Path)
		}
		if filepath.Clean(rule.Path) != rule.Path {
			return fmt.Errorf("sandbox init Landlock path must be clean, got %q", rule.Path)
		}
		switch rule.Scope {
		case LandlockScopeRead, LandlockScopeReadExecute, LandlockScopeReadWrite:
		default:
			return fmt.Errorf("sandbox init Landlock rule %q has unknown scope %q", rule.Path, rule.Scope)
		}
		if _, ok := seen[rule]; ok {
			return fmt.Errorf("sandbox init Landlock rule for %q is duplicated", rule.Path)
		}
		seen[rule] = struct{}{}
	}
	return nil
}

// defaultLandlockSystemRules is the read-only baseline the guest runtime needs.
// It intentionally omits the init binary directory (/.agent-compose) and the
// control directory: a workload that cannot see them cannot replace the init
// binary or the control socket. Landlock does not mediate connect(2) on UNIX
// sockets, which is why the control socket must move out of the workload's
// reach in the supervisor design rather than rely on this ruleset.
var defaultLandlockSystemRules = []LandlockRule{
	{Path: "/usr", Scope: LandlockScopeReadExecute},
	{Path: "/bin", Scope: LandlockScopeReadExecute},
	{Path: "/sbin", Scope: LandlockScopeReadExecute},
	{Path: "/lib", Scope: LandlockScopeReadExecute},
	{Path: "/lib32", Scope: LandlockScopeReadExecute},
	{Path: "/lib64", Scope: LandlockScopeReadExecute},
	{Path: "/libx32", Scope: LandlockScopeReadExecute},
	{Path: "/opt", Scope: LandlockScopeReadExecute},
	{Path: "/etc", Scope: LandlockScopeRead},
	{Path: "/var", Scope: LandlockScopeRead},
	{Path: "/sys", Scope: LandlockScopeRead},
	{Path: "/dev", Scope: LandlockScopeReadWrite},
	{Path: "/proc", Scope: LandlockScopeReadWrite},
}

// landlockReadOnlyFiles covers files that are commonly bind-mounted over their
// parent directory (Docker mounts /etc/resolv.conf, /etc/hosts, and
// /etc/hostname this way). A rule on the parent directory does not cover the
// inode of a separate mount, so those files get their own file rules.
var landlockReadOnlyFiles = []string{
	"/etc/resolv.conf",
	"/etc/hosts",
	"/etc/hostname",
	"/etc/localtime",
	"/etc/ld.so.cache",
	"/etc/nsswitch.conf",
	"/etc/passwd",
	"/etc/group",
	"/etc/ssl/certs/ca-certificates.crt",
}

// landlockEnvironmentWritablePaths maps the guest livespace variables the
// drivers already export to the directories a workload may modify. The
// fallbacks match pkg/config's guest path defaults; drivers set the variables
// explicitly, so the fallback only matters for a hand-built init invocation.
var landlockEnvironmentWritablePaths = []struct {
	name     string
	fallback string
}{
	{name: "WORKSPACE", fallback: "/workspace"},
	{name: "HOME", fallback: "/root"},
	{name: "STATE_ROOT", fallback: "/data/state"},
	{name: "RUNTIME_ROOT", fallback: "/data/runtime"},
	{name: "LOG_ROOT", fallback: "/data/logs"},
	{name: "TMPDIR", fallback: "/tmp"},
}

// DefaultLandlockPolicy builds the baseline ruleset from a process environment.
// The result is deterministic for one environment and contains no filesystem
// I/O; path availability is checked by the Linux enforcement code.
func DefaultLandlockPolicy(environ []string) (LandlockPolicy, error) {
	env := make(map[string]string, len(environ))
	for _, item := range environ {
		if key, value, ok := strings.Cut(item, "="); ok {
			env[key] = value
		}
	}
	rules := make([]LandlockRule, 0, len(defaultLandlockSystemRules)+len(landlockEnvironmentWritablePaths)+len(landlockReadOnlyFiles)+1)
	rules = append(rules, defaultLandlockSystemRules...)
	for _, path := range landlockReadOnlyFiles {
		rules = append(rules, LandlockRule{Path: path, Scope: LandlockScopeRead})
	}
	rules = append(rules, LandlockRule{Path: "/var/tmp", Scope: LandlockScopeReadWrite})
	for _, entry := range landlockEnvironmentWritablePaths {
		path := strings.TrimSpace(env[entry.name])
		if path == "" {
			path = entry.fallback
		}
		if !filepath.IsAbs(path) {
			return LandlockPolicy{}, fmt.Errorf("%s must be an absolute path for Landlock, got %q", entry.name, path)
		}
		rules = append(rules, LandlockRule{Path: filepath.Clean(path), Scope: LandlockScopeReadWrite})
	}
	policy := LandlockPolicy{Rules: rules}
	if err := policy.Validate(); err != nil {
		return LandlockPolicy{}, err
	}
	return policy, nil
}

// decodeLandlockPolicy parses and validates the trampoline's ruleset. A missing
// or malformed policy is an error: enforce mode must never fall back to an
// unrestricted exec because init and the trampoline disagreed.
func decodeLandlockPolicy(raw string) (LandlockPolicy, error) {
	if strings.TrimSpace(raw) == "" {
		return LandlockPolicy{}, errors.New("sandbox init Landlock policy is missing")
	}
	var policy LandlockPolicy
	if err := json.Unmarshal([]byte(raw), &policy); err != nil {
		return LandlockPolicy{}, fmt.Errorf("decode sandbox init Landlock policy: %w", err)
	}
	if err := policy.Validate(); err != nil {
		return LandlockPolicy{}, err
	}
	return policy, nil
}
