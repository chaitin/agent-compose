package llms

import (
	"sort"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// MergeManagedExecEnv layers the daemon-managed LLM environment over the base
// environment an execution was built with. The managed values win per key.
//
// The base environment is copied in full, including provider keys. An earlier
// revision stripped provider keys from the base so a sandbox's own provider
// environment could not leak past a daemon-managed facade, where the managed
// token was the only credential that was supposed to reach the guest. That
// stripping is wrong under direct mode: an agent that declares its own upstream
// is deliberately served by that declaration, with its real credential in the
// guest, so blanking the base is exactly what would break it. Managed values
// still overwrite the keys the daemon owns.
func MergeManagedExecEnv(base map[string]string, managed map[string]string) map[string]string {
	if len(base) == 0 && len(managed) == 0 {
		return nil
	}
	result := make(map[string]string, len(base)+len(managed))
	for key, value := range base {
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
