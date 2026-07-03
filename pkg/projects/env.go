package projects

import (
	"slices"

	"agent-compose/pkg/compose"
	domain "agent-compose/pkg/model"
)

func SessionEnvItemsFromCompose(values map[string]compose.EnvVarSpec) []domain.SessionEnvVar {
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	slices.Sort(names)
	items := make([]domain.SessionEnvVar, 0, len(values))
	for _, name := range names {
		value := values[name]
		items = append(items, domain.SessionEnvVar{Name: name, Value: value.Value, Secret: value.Secret})
	}
	return items
}
