package egress

import (
	"errors"
	"fmt"
	"path"
	"slices"
	"strings"
)

// MaxBinaryPathsPerEntry bounds how many executable paths one declared
// allowance or denial may name. Every path is validated and compiled before a
// declaration can be refused, so an unbounded list would be work one request
// asks the daemon to do. The limit is far above any real declaration.
const MaxBinaryPathsPerEntry = 64

// maxBinaryPathLength bounds one executable path. Linux PATH_MAX is 4096 bytes,
// and a longer value could never name a process executable.
const maxBinaryPathLength = 4096

// NormalizeBinaryPath validates one declared executable path and returns its
// canonical form.
//
// The path must be absolute and must not contain "..". It is cleaned with the
// path package (not filepath) because the declaration names a path inside the
// Linux guest, regardless of the host the daemon runs on. Leading or trailing
// whitespace and control characters are rejected rather than trimmed: a path is
// matched literally against the executable the kernel reports, so silently
// changing it would make a declaration that appears to apply never match.
func NormalizeBinaryPath(value string) (string, error) {
	if strings.TrimSpace(value) == "" {
		return "", errors.New("binary path must not be empty")
	}
	if strings.TrimSpace(value) != value {
		return "", fmt.Errorf("binary path %q must not have leading or trailing whitespace", value)
	}
	if len(value) > maxBinaryPathLength {
		return "", fmt.Errorf("binary path is longer than %d bytes", maxBinaryPathLength)
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return "", fmt.Errorf("binary path %q must not contain a NUL, carriage return, or newline", value)
	}
	if !path.IsAbs(value) {
		return "", fmt.Errorf("binary path %q must be absolute", value)
	}
	for _, element := range strings.Split(value, "/") {
		if element == ".." {
			return "", fmt.Errorf("binary path %q must not contain %q", value, "..")
		}
	}
	cleaned := path.Clean(value)
	if len(cleaned) > maxBinaryPathLength {
		return "", fmt.Errorf("binary path is longer than %d bytes", maxBinaryPathLength)
	}
	return cleaned, nil
}

// NormalizeBinaries validates a declared executable-path list, canonicalizes
// every entry, rejects duplicates, and sorts the result so the same declaration
// always compiles to the same policy generation. A nil list stays nil: an
// unscoped entry is a different declaration from one whose list is empty after
// validation.
func NormalizeBinaries(values []string) ([]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > MaxBinaryPathsPerEntry {
		return nil, fmt.Errorf("at most %d binaries are supported per entry, got %d", MaxBinaryPathsPerEntry, len(values))
	}
	normalized := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		canonical, err := NormalizeBinaryPath(value)
		if err != nil {
			return nil, fmt.Errorf("binaries[%d]: %w", index, err)
		}
		if _, duplicate := seen[canonical]; duplicate {
			continue
		}
		seen[canonical] = struct{}{}
		normalized = append(normalized, canonical)
	}
	slices.Sort(normalized)
	return normalized, nil
}

// BinaryScoped reports whether a rule or entry declares executable paths.
func BinaryScoped(binaries []string) bool {
	return len(binaries) > 0
}

// BinaryMatches reports whether the request binary is one the rule declares.
// An empty request binary matches nothing: an executable the engine could not
// establish must never satisfy a binary-scoped allowance.
func BinaryMatches(requestBinary string, binaries []string) bool {
	if requestBinary == "" {
		return false
	}
	return slices.Contains(binaries, requestBinary)
}

// BinaryScopedAllowances counts the allowances scoped to a declared binary. A
// mechanism that cannot attribute a connection to an executable must know how
// many entries it is applying only partially.
func BinaryScopedAllowances(allow []AllowEntry) int {
	scoped := 0
	for _, entry := range allow {
		if BinaryScoped(entry.Binaries) {
			scoped++
		}
	}
	return scoped
}

// BinaryScopedDenials counts the denials scoped to a declared binary.
func BinaryScopedDenials(deny []DenyEntry) int {
	scoped := 0
	for _, entry := range deny {
		if BinaryScoped(entry.Binaries) {
			scoped++
		}
	}
	return scoped
}
