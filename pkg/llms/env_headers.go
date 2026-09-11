package llms

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/textproto"
	"strings"

	"golang.org/x/net/http/httpguts"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

const llmAPIHeadersEnv = "LLM_API_HEADERS"

func envProviderHeadersJSON(lookup EnvProviderLookup, extra map[string]string) (string, error) {
	headers := make(map[string]string, len(extra))
	for key, value := range extra {
		canonical, err := canonicalEnvProviderHeader(key, value)
		if err != nil {
			return "", err
		}
		if _, exists := headers[canonical]; exists {
			return "", envProviderHeadersError("provider headers contain duplicate header names", nil)
		}
		headers[canonical] = value
	}
	raw := ""
	if lookup != nil {
		raw = strings.TrimSpace(lookup(llmAPIHeadersEnv))
	}
	if raw != "" {
		custom, err := decodeEnvProviderHeaders(raw)
		if err != nil {
			return "", err
		}
		for key, value := range custom {
			headers[key] = value
		}
	}
	if len(headers) == 0 {
		return "{}", nil
	}
	encoded, err := json.Marshal(headers)
	if err != nil {
		return "", envProviderHeadersError(fmt.Sprintf("encode %s", llmAPIHeadersEnv), err)
	}
	return string(encoded), nil
}

func decodeEnvProviderHeaders(raw string) (map[string]string, error) {
	decoder := json.NewDecoder(strings.NewReader(raw))
	token, err := decoder.Token()
	if err != nil {
		return nil, envProviderHeadersError(fmt.Sprintf("decode %s", llmAPIHeadersEnv), err)
	}
	if delimiter, ok := token.(json.Delim); !ok || delimiter != '{' {
		return nil, envProviderHeadersError(fmt.Sprintf("%s must be a JSON object", llmAPIHeadersEnv), nil)
	}

	headers := make(map[string]string)
	for decoder.More() {
		nameToken, err := decoder.Token()
		if err != nil {
			return nil, envProviderHeadersError(fmt.Sprintf("decode %s", llmAPIHeadersEnv), err)
		}
		name, ok := nameToken.(string)
		if !ok {
			return nil, envProviderHeadersError(fmt.Sprintf("%s contains an invalid header name", llmAPIHeadersEnv), nil)
		}
		var rawValue any
		if err := decoder.Decode(&rawValue); err != nil {
			return nil, envProviderHeadersError(fmt.Sprintf("decode %s", llmAPIHeadersEnv), err)
		}
		value, ok := rawValue.(string)
		if !ok {
			return nil, envProviderHeadersError(fmt.Sprintf("%s header values must be strings", llmAPIHeadersEnv), nil)
		}
		canonical, err := canonicalEnvProviderHeader(name, value)
		if err != nil {
			return nil, err
		}
		if _, exists := headers[canonical]; exists {
			return nil, envProviderHeadersError(fmt.Sprintf("%s contains duplicate header names", llmAPIHeadersEnv), nil)
		}
		headers[canonical] = value
	}
	if _, err := decoder.Token(); err != nil {
		return nil, envProviderHeadersError(fmt.Sprintf("decode %s", llmAPIHeadersEnv), err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			err = fmt.Errorf("multiple JSON values")
		}
		return nil, envProviderHeadersError(fmt.Sprintf("decode %s", llmAPIHeadersEnv), err)
	}
	return headers, nil
}

func canonicalEnvProviderHeader(name, value string) (string, error) {
	if name != strings.TrimSpace(name) || !httpguts.ValidHeaderFieldName(name) {
		return "", envProviderHeadersError(fmt.Sprintf("%s contains an invalid header name", llmAPIHeadersEnv), nil)
	}
	if !httpguts.ValidHeaderFieldValue(value) {
		return "", envProviderHeadersError(fmt.Sprintf("%s contains an invalid header value", llmAPIHeadersEnv), nil)
	}
	return textproto.CanonicalMIMEHeaderKey(name), nil
}

func envProviderHeadersError(reason string, cause error) error {
	return domain.ClassifyError(domain.ErrFailedPrecondition, reason, cause)
}
