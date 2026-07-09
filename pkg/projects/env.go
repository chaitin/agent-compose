package projects

import (
	"slices"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
)

func SandboxEnvItemsFromCompose(values map[string]compose.EnvVarSpec) []domain.SandboxEnvVar {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	items := make([]domain.SandboxEnvVar, 0, len(values))
	for _, name := range names {
		value := values[name]
		items = append(items, domain.SandboxEnvVar{Name: name, Value: value.Value, Secret: value.Secret})
	}
	return items
}
