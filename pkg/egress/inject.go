package egress

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"golang.org/x/net/http/httpguts"
)

// Limits bound the credential-injection surface of one allowance. Header names
// and templates are bounded so a declaration cannot make the proxy do
// unbounded work, and templates may only reference environment variables: a
// literal secret in a project file would be copied into the spec hash and every
// report, so the declaration carries a reference rather than the value.
const (
	MaxInjectedHeaders     = 16
	MaxInjectedHeaderValue = 4096
)

// NormalizeInjectHeaders validates and canonicalizes injected header templates.
// A value must contain at least one ${ENV_VAR} reference; the proxy resolves it
// from its own environment at request time and never logs the resolved value.
func NormalizeInjectHeaders(values map[string]string) (map[string]string, error) {
	if len(values) == 0 {
		return nil, nil
	}
	if len(values) > MaxInjectedHeaders {
		return nil, fmt.Errorf("at most %d injected headers are supported, got %d", MaxInjectedHeaders, len(values))
	}
	normalized := make(map[string]string, len(values))
	for name, value := range values {
		canonical := http.CanonicalHeaderKey(strings.TrimSpace(name))
		if !httpguts.ValidHeaderFieldName(canonical) {
			return nil, fmt.Errorf("injected header name %q is invalid", name)
		}
		if forbiddenInjectedHeader(canonical) {
			return nil, fmt.Errorf("injected header %q is managed by the engine or the HTTP transport", canonical)
		}
		if len(value) > MaxInjectedHeaderValue {
			return nil, fmt.Errorf("injected header %q value is longer than %d bytes", canonical, MaxInjectedHeaderValue)
		}
		if !httpguts.ValidHeaderFieldValue(value) {
			return nil, fmt.Errorf("injected header %q value contains an invalid character", canonical)
		}
		if !strings.Contains(value, "${") {
			return nil, fmt.Errorf("injected header %q must reference an environment variable with ${NAME}", canonical)
		}
		if _, err := expandInjectHeaderValue(value, func(string) (string, bool) { return "x", true }); err != nil {
			return nil, fmt.Errorf("injected header %q: %w", canonical, err)
		}
		if _, exists := normalized[canonical]; exists {
			return nil, fmt.Errorf("duplicate injected header %q", canonical)
		}
		normalized[canonical] = value
	}
	return normalized, nil
}

func forbiddenInjectedHeader(name string) bool {
	switch http.CanonicalHeaderKey(name) {
	case "Host", "Content-Length", "Transfer-Encoding", "Connection", "Proxy-Connection",
		"Proxy-Authorization", "Trailer", "Upgrade", "Keep-Alive", "Te":
		return true
	}
	return strings.HasPrefix(http.CanonicalHeaderKey(name), "Proxy-") ||
		strings.HasPrefix(http.CanonicalHeaderKey(name), "X-Agent-Compose-")
}

// ExpandInjectHeaders resolves templates against the proxy environment. A
// missing or empty variable fails closed: forwarding a request without the
// credential the policy promised would turn a policy denial into an
// unauthenticated upstream request.
func ExpandInjectHeaders(templates map[string]string, lookup func(string) (string, bool)) (map[string]string, error) {
	if len(templates) == 0 {
		return nil, nil
	}
	if lookup == nil {
		lookup = func(string) (string, bool) { return "", false }
	}
	resolved := make(map[string]string, len(templates))
	for name, template := range templates {
		value, err := expandInjectHeaderValue(template, lookup)
		if err != nil {
			return nil, fmt.Errorf("resolve injected header %q: %w", name, err)
		}
		resolved[name] = value
	}
	return resolved, nil
}

func expandInjectHeaderValue(template string, lookup func(string) (string, bool)) (string, error) {
	var builder strings.Builder
	rest := template
	found := false
	for {
		start := strings.Index(rest, "${")
		if start < 0 {
			builder.WriteString(rest)
			break
		}
		end := strings.Index(rest[start+2:], "}")
		if end < 0 {
			return "", fmt.Errorf("unterminated environment reference")
		}
		end += start + 2
		name := rest[start+2 : end]
		if !validEnvName(name) {
			return "", fmt.Errorf("invalid environment reference %q", name)
		}
		value, ok := lookup(name)
		if !ok || value == "" {
			return "", fmt.Errorf("environment variable %q is not set", name)
		}
		builder.WriteString(rest[:start])
		builder.WriteString(value)
		found = true
		rest = rest[end+1:]
	}
	if !found {
		return "", fmt.Errorf("template has no environment reference")
	}
	resolved := builder.String()
	if !httpguts.ValidHeaderFieldValue(resolved) {
		return "", fmt.Errorf("resolved header value contains an invalid character")
	}
	return resolved, nil
}

func validEnvName(name string) bool {
	if name == "" {
		return false
	}
	for index, char := range name {
		if char == '_' || (char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (index > 0 && char >= '0' && char <= '9') {
			continue
		}
		return false
	}
	return true
}

// InjectedHeaderNames returns the canonical names in stable order. Reports and
// audit lines use names only; the resolved values never leave the proxy.
func InjectedHeaderNames(values map[string]string) []string {
	if len(values) == 0 {
		return nil
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
