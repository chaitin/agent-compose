package llms

import (
	"fmt"
	"os"
	"strings"
)

// A guest provider configuration file can hold two writers' output at once: the
// managed MCP writer owns the region between its markers, and the LLM dialect
// writer owns everything around it. The functions here are what let both
// writers share one file — each rewrites its own region and carries the other's
// across untouched.
//
// The writers do not run in a fixed order. Starting a sandbox writes the MCP
// region after the dialect configuration, and an interactive prompt attach
// refreshes the dialect configuration after the MCP region, so neither writer
// may assume the other has already run.

// managedTextBlockRange returns the half-open range of the marker-delimited
// region of existing, including both markers and the newline that ends the
// region. ok is false when either marker is missing.
func managedTextBlockRange(existing, startMarker, endMarker string) (start, end int, ok bool) {
	start = strings.Index(existing, startMarker)
	if start < 0 {
		return 0, 0, false
	}
	end = strings.Index(existing[start:], endMarker)
	if end < 0 {
		return 0, 0, false
	}
	end += start + len(endMarker)
	if end < len(existing) && existing[end] == '\n' {
		end++
	}
	return start, end, true
}

// replaceManagedTextBlock replaces the marker-delimited region of existing with
// managed, which may be empty to delete the region. Content outside the region
// is preserved, which is what keeps the dialect writer's output across an MCP
// rewrite.
//
// An unterminated region — a start marker with no end marker — is treated as
// extending to the end of the file, so a partly written region cannot survive
// as unmanaged content.
func replaceManagedTextBlock(existing, startMarker, endMarker, managed string) string {
	if start, end, ok := managedTextBlockRange(existing, startMarker, endMarker); ok {
		existing = existing[:start] + existing[end:]
	} else if start := strings.Index(existing, startMarker); start >= 0 {
		existing = existing[:start]
	}
	existing = strings.TrimRight(existing, "\n")
	managed = strings.TrimSpace(managed)
	if managed == "" {
		if existing == "" {
			return ""
		}
		return existing + "\n"
	}
	if existing == "" {
		return managed + "\n"
	}
	return existing + "\n\n" + managed + "\n"
}

// preserveManagedTextBlock returns payload with the marker-delimited region of
// the file at path appended, so a writer that replaces the whole file keeps the
// region another writer owns.
//
// A missing file is not an error: the region does not exist yet, and the writer
// is creating the file. Any other read failure is reported instead of silently
// dropping a region the guest needs.
func preserveManagedTextBlock(path, payload, startMarker, endMarker string) (string, error) {
	existing, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return payload, nil
		}
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	start, end, ok := managedTextBlockRange(string(existing), startMarker, endMarker)
	if !ok {
		return payload, nil
	}
	block := strings.TrimRight(string(existing[start:end]), "\n")
	if block == "" {
		return payload, nil
	}
	return strings.TrimRight(payload, "\n") + "\n\n" + block + "\n", nil
}
