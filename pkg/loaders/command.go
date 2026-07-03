package loaders

import (
	"fmt"
	"strings"

	domain "agent-compose/pkg/model"
)

func ValidateCommandRequest(request domain.LoaderCommandRequest) error {
	switch strings.ToLower(strings.TrimSpace(request.Mode)) {
	case "exec":
		if strings.TrimSpace(request.Command) == "" {
			return fmt.Errorf("command is required")
		}
	case "shell":
		if strings.TrimSpace(request.Script) == "" {
			return fmt.Errorf("script is required")
		}
	default:
		return fmt.Errorf("loader command mode must be exec or shell")
	}
	return nil
}

func CommandCellSource(request domain.LoaderCommandRequest) string {
	if strings.EqualFold(strings.TrimSpace(request.Mode), "shell") {
		return request.Script
	}
	items := append([]string{request.Command}, request.Args...)
	return strings.Join(items, " ")
}

func CommandRequestRequiresCleanup(loader domain.Loader, request domain.LoaderCommandRequest) bool {
	effectivePolicy := domain.NormalizeLoaderSessionPolicy(loader.Summary.SessionPolicy)
	if strings.TrimSpace(request.SessionPolicy) != "" {
		effectivePolicy = domain.NormalizeLoaderSessionPolicy(request.SessionPolicy)
	}
	return effectivePolicy == domain.LoaderSessionPolicyNew || CommandRequestOverridesSession(request)
}

func CommandRequestOverridesSession(request domain.LoaderCommandRequest) bool {
	return strings.TrimSpace(request.Driver) != "" ||
		strings.TrimSpace(request.GuestImage) != "" ||
		strings.TrimSpace(request.WorkspaceID) != "" ||
		len(domain.NormalizeEnvItems(request.SessionEnv)) > 0
}
