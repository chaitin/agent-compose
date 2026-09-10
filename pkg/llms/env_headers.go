package llms

import (
	"encoding/json"
	"fmt"
	"strings"
)

const llmAPIHeadersEnv = "LLM_API_HEADERS"

func envProviderHeadersJSON(lookup EnvProviderLookup, extra map[string]string) (string, error) {
	headers := make(map[string]string, len(extra))
	for key, value := range extra {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		headers[key] = value
	}
	raw := ""
	if lookup != nil {
		raw = strings.TrimSpace(lookup(llmAPIHeadersEnv))
	}
	if raw != "" {
		if !strings.HasPrefix(raw, "{") || !strings.HasSuffix(raw, "}") {
			return "", fmt.Errorf("%s must be a JSON object", llmAPIHeadersEnv)
		}
		custom := map[string]string{}
		if err := json.Unmarshal([]byte(raw), &custom); err != nil {
			return "", fmt.Errorf("decode %s: %w", llmAPIHeadersEnv, err)
		}
		for key, value := range custom {
			if strings.ContainsAny(key, "\r\n") || strings.ContainsAny(value, "\r\n") {
				return "", fmt.Errorf("%s must not contain CR or LF", llmAPIHeadersEnv)
			}
			key = strings.TrimSpace(key)
			if key == "" {
				return "", fmt.Errorf("%s contains an empty header name", llmAPIHeadersEnv)
			}
			headers[key] = value
		}
	}
	if len(headers) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(headers)
	if err != nil {
		return "", fmt.Errorf("encode %s: %w", llmAPIHeadersEnv, err)
	}
	return string(encoded), nil
}
