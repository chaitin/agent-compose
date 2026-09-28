package schedulers

import (
	"fmt"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"sort"
	"strings"
	"time"
)

func schedulerDurationOption(options map[string]any, keys ...string) (time.Duration, error) {
	for _, key := range keys {
		value, ok := options[key]
		if !ok || value == nil {
			continue
		}
		switch raw := value.(type) {
		case string:
			trimmed := strings.TrimSpace(raw)
			if trimmed == "" {
				return 0, nil
			}
			parsed, err := time.ParseDuration(trimmed)
			if err != nil {
				return 0, err
			}
			if parsed <= 0 {
				return 0, fmt.Errorf("duration must be positive")
			}
			return parsed, nil
		default:
			return 0, fmt.Errorf("duration must be a string")
		}
	}
	return 0, nil
}

func schedulerStringOption(options map[string]any, keys ...string) string {
	for _, key := range keys {
		value, ok := options[key]
		if !ok || value == nil {
			continue
		}
		if raw, ok := value.(string); ok {
			return strings.TrimSpace(raw)
		}
	}
	return ""
}

func schedulerBoolOption(options map[string]any, keys ...string) bool {
	for _, key := range keys {
		value, ok := options[key]
		if !ok || value == nil {
			continue
		}
		if raw, ok := value.(bool); ok {
			return raw
		}
	}
	return false
}

func schedulerStringArrayOption(options map[string]any, keys ...string) ([]string, error) {
	for _, key := range keys {
		value, ok := options[key]
		if !ok || value == nil {
			continue
		}
		rawItems, ok := value.([]any)
		if !ok {
			return nil, fmt.Errorf("must be an array")
		}
		items := make([]string, 0, len(rawItems))
		for index, rawItem := range rawItems {
			if rawItem == nil {
				return nil, fmt.Errorf("item %d must be a string", index)
			}
			item, ok := rawItem.(string)
			if !ok {
				return nil, fmt.Errorf("item %d must be a string", index)
			}
			items = append(items, item)
		}
		return items, nil
	}
	return nil, nil
}

func schedulerStringMapOption(options map[string]any, keys ...string) (map[string]string, error) {
	for _, key := range keys {
		value, ok := options[key]
		if !ok || value == nil {
			continue
		}
		rawItems, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("must be an object")
		}
		items := make(map[string]string, len(rawItems))
		for rawName, rawValue := range rawItems {
			name := strings.TrimSpace(rawName)
			if name == "" {
				continue
			}
			if rawValue == nil {
				items[name] = ""
				continue
			}
			value, ok := rawValue.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a string", name)
			}
			items[name] = value
		}
		return items, nil
	}
	return nil, nil
}

func schedulerInt64Option(options map[string]any, keys ...string) (int64, error) {
	for _, key := range keys {
		value, ok := options[key]
		if !ok || value == nil {
			continue
		}
		switch typed := value.(type) {
		case float64:
			return int64(typed), nil
		case int64:
			return typed, nil
		case int:
			return int64(typed), nil
		default:
			return 0, fmt.Errorf("must be a number")
		}
	}
	return 0, nil
}

func schedulerSandboxPolicyOption(options map[string]any, state *schedulerExecutionState, apiName string) string {
	for _, key := range []string{"sandboxPolicy", "sandbox_policy"} {
		if value := schedulerStringOption(options, key); strings.TrimSpace(value) != "" {
			return normalizeSchedulerSandboxPolicy(value)
		}
	}
	return ""
}

func schedulerSandboxEnvOption(options map[string]any, state *schedulerExecutionState, apiName string) ([]domain.SandboxEnvVar, error) {
	if items, ok, err := schedulerEnvOption(options, []string{"sandboxEnv", "sandbox_env"}, apiName, "sandboxEnv"); ok || err != nil {
		return items, err
	}
	return nil, nil
}

func schedulerEnvOption(options map[string]any, keys []string, apiName, label string) ([]domain.SandboxEnvVar, bool, error) {
	for _, key := range keys {
		value, ok := options[key]
		if !ok {
			continue
		}
		items, err := schedulerSandboxEnvItems(value)
		if err != nil {
			return nil, true, fmt.Errorf("decode %s %s: %w", apiName, label, err)
		}
		return items, true, nil
	}
	return nil, false, nil
}

func schedulerSandboxEnvItems(value any) ([]domain.SandboxEnvVar, error) {
	switch typed := value.(type) {
	case nil:
		return nil, nil
	case map[string]any:
		keys := make([]string, 0, len(typed))
		for key := range typed {
			if strings.TrimSpace(key) == "" {
				continue
			}
			keys = append(keys, key)
		}
		sort.Strings(keys)
		items := make([]domain.SandboxEnvVar, 0, len(keys))
		for _, key := range keys {
			name := strings.TrimSpace(key)
			if name == "" {
				continue
			}
			envValue, secret, err := schedulerSandboxEnvValue(name, typed[key])
			if err != nil {
				return nil, fmt.Errorf("%s: %w", name, err)
			}
			items = append(items, domain.SandboxEnvVar{Name: name, Value: envValue, Secret: secret})
		}
		return normalizeEnvItems(items), nil
	case []any:
		items := make([]domain.SandboxEnvVar, 0, len(typed))
		for index, rawItem := range typed {
			entry, ok := rawItem.(map[string]any)
			if !ok {
				return nil, fmt.Errorf("item %d must be an object", index)
			}
			name := schedulerStringOption(entry, "name")
			if name == "" {
				return nil, fmt.Errorf("item %d requires a non-empty name", index)
			}
			envValue, secret, err := schedulerSandboxEnvValue(name, entry["value"])
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", index, err)
			}
			if rawSecret, ok := entry["secret"]; ok && rawSecret != nil {
				switch typedSecret := rawSecret.(type) {
				case bool:
					secret = typedSecret
				case string:
					secret = strings.EqualFold(strings.TrimSpace(typedSecret), "true")
				case float64:
					secret = typedSecret != 0
				default:
					return nil, fmt.Errorf("item %d secret must be a boolean", index)
				}
			}
			items = append(items, domain.SandboxEnvVar{Name: name, Value: envValue, Secret: secret})
		}
		return normalizeEnvItems(items), nil
	default:
		return nil, fmt.Errorf("must be an object map or array")
	}
}

func schedulerSandboxEnvValue(name string, value any) (string, bool, error) {
	secret := schedulerSecretEnvName(name)
	switch typed := value.(type) {
	case nil:
		return "", secret, nil
	case string:
		return typed, secret, nil
	case bool:
		if typed {
			return "true", secret, nil
		}
		return "false", secret, nil
	case float64:
		return strings.TrimSpace(strings.TrimRight(strings.TrimRight(fmt.Sprintf("%f", typed), "0"), ".")), secret, nil
	case map[string]any:
		envValue, nestedSecret, err := schedulerSandboxEnvValue(name, typed["value"])
		if err != nil {
			return "", false, err
		}
		secret = nestedSecret
		if rawSecret, ok := typed["secret"]; ok && rawSecret != nil {
			switch typedSecret := rawSecret.(type) {
			case bool:
				secret = typedSecret
			case string:
				secret = strings.EqualFold(strings.TrimSpace(typedSecret), "true")
			case float64:
				secret = typedSecret != 0
			default:
				return "", false, fmt.Errorf("secret must be a boolean")
			}
		}
		return envValue, secret, nil
	default:
		return fmt.Sprint(typed), secret, nil
	}
}

func schedulerSecretEnvName(name string) bool {
	name = strings.ToUpper(strings.TrimSpace(name))
	if name == "" {
		return false
	}
	if strings.Contains(name, "PASSWORD") || strings.HasSuffix(name, "_TOKEN") || strings.HasSuffix(name, "_SECRET") || strings.HasSuffix(name, "_KEY") {
		return true
	}
	switch name {
	case "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "LLM_API_KEY":
		return true
	default:
		return false
	}
}

func normalizeImagePullPolicy(policy string) string {
	switch strings.ToLower(strings.TrimSpace(policy)) {
	case "always":
		return "always"
	case "missing":
		return "missing"
	case "never":
		return "never"
	default:
		return ""
	}
}
