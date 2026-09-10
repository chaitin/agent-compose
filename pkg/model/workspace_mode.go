package model

import (
	"fmt"
	"strings"
)

const (
	WorkspaceModeCopy  = "copy"
	WorkspaceModeMount = "mount"
)

// NormalizeWorkspaceMode preserves the historical empty representation of copy
// so adding an explicit default does not change a project's canonical hash.
func NormalizeWorkspaceMode(raw string) (string, error) {
	switch mode := strings.ToLower(strings.TrimSpace(raw)); mode {
	case "", WorkspaceModeCopy:
		return "", nil
	case WorkspaceModeMount:
		return mode, nil
	default:
		return "", fmt.Errorf("%w: unsupported workspace mode %q", ErrInvalidArgument, raw)
	}
}

// NormalizeWorkspaceDelivery validates the source-independent delivery contract.
// Source-specific configuration and runtime capability are checked by their owners.
func NormalizeWorkspaceDelivery(provider, mode string, readOnly bool) (string, error) {
	normalized, err := NormalizeWorkspaceMode(mode)
	if err != nil {
		return "", err
	}
	if normalized == WorkspaceModeMount && !strings.EqualFold(strings.TrimSpace(provider), "file") {
		return "", fmt.Errorf("%w: workspace mount mode requires provider file", ErrInvalidArgument)
	}
	if normalized == "" && readOnly {
		return "", fmt.Errorf("%w: workspace read_only requires mount mode", ErrInvalidArgument)
	}
	return normalized, nil
}
