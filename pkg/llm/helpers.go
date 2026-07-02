package llm

import (
	"path/filepath"
	"strings"

	appconfig "agent-compose/pkg/config"
	"agent-compose/pkg/model"
)

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func normalizeEnvItems(items []SessionEnvVar) []SessionEnvVar {
	return model.NormalizeSessionEnvItems(items)
}

func normalizeAgentKind(agent string) string {
	return model.NormalizeAgentProvider(agent)
}

func hostSessionDir(session *Session) string {
	return filepath.Dir(session.Summary.WorkspacePath)
}

func hostSessionHome(session *Session) string {
	return filepath.Join(hostSessionDir(session), "home")
}

func guestSessionHome(config *appconfig.Config) string {
	return config.GuestHomePath
}
