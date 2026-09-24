package llms

import (
	"sort"

	driverpkg "github.com/chaitin/agent-compose/pkg/driver"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// MergeManagedExecEnv layers the daemon-managed LLM environment over the base
// environment an execution was built with. The managed values win per key.
//
// Provider credential names are dropped from the base before the managed layer
// is applied. A sandbox may carry a credential an operator declared in its
// project or agent environment, and that value must never reach the guest: the
// only credential a guest may present is the facade token the managed layer
// installs under the vendor's conventional variable. Stripping before the merge
// keeps that token, because the managed layer writes the names the daemon owns.
func MergeManagedExecEnv(base map[string]string, managed map[string]string) map[string]string {
	if len(base) == 0 && len(managed) == 0 {
		return nil
	}
	result := make(map[string]string, len(base)+len(managed))
	for key, value := range base {
		if driverpkg.LLMProviderKeyName(key) {
			continue
		}
		result[key] = value
	}
	for key, value := range managed {
		result[key] = value
	}
	return result
}

func EnvItemsFromMap(values map[string]string, secret bool) []domain.SandboxEnvVar {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	items := make([]domain.SandboxEnvVar, 0, len(keys))
	for _, key := range keys {
		items = append(items, domain.SandboxEnvVar{Name: key, Value: values[key], Secret: secret})
	}
	return items
}
