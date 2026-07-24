package compose

import (
	"net/url"
	"slices"
	"strconv"
	"strings"

	"agent-compose/pkg/capability"
)

func normalizeOctoBusMap(path string, values map[string]OctoBusServerSpec, options NormalizeOptions) (map[string]NormalizedOctoBusServerSpec, error) {
	if len(values) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	normalized := make(map[string]NormalizedOctoBusServerSpec, len(values))
	for _, key := range keys {
		serverPath := joinPath(path, key)
		if err := validateStableIdentifier(serverPath, key, "octobus server name"); err != nil {
			return nil, err
		}
		server, err := normalizeOctoBusServer(serverPath, values[key], options)
		if err != nil {
			return nil, err
		}
		normalized[key] = server
	}
	return normalized, nil
}

func normalizeOctoBusServer(path string, value OctoBusServerSpec, options NormalizeOptions) (NormalizedOctoBusServerSpec, error) {
	rawURL, err := interpolateEnvValue(path+".url", strings.TrimSpace(value.URL), options)
	if err != nil {
		return NormalizedOctoBusServerSpec{}, err
	}
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return NormalizedOctoBusServerSpec{}, &ValidationError{Path: path + ".url", Message: "url is required"}
	}
	parsed, err := url.Parse(rawURL)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return NormalizedOctoBusServerSpec{}, &ValidationError{Path: path + ".url", Message: "url must be an absolute URL"}
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return NormalizedOctoBusServerSpec{}, &ValidationError{Path: path + ".url", Message: "url scheme must be http or https"}
	}
	if parsed.User != nil {
		return NormalizedOctoBusServerSpec{}, &ValidationError{Path: path + ".url", Message: "url must not include userinfo"}
	}
	if parsed.Path != "" && parsed.Path != "/" {
		return NormalizedOctoBusServerSpec{}, &ValidationError{Path: path + ".url", Message: "url path must be empty or /"}
	}
	if parsed.RawQuery != "" {
		return NormalizedOctoBusServerSpec{}, &ValidationError{Path: path + ".url", Message: "url must not include a query"}
	}
	if parsed.Fragment != "" {
		return NormalizedOctoBusServerSpec{}, &ValidationError{Path: path + ".url", Message: "url must not include a fragment"}
	}
	token, err := interpolateEnvValue(path+".token", strings.TrimSpace(value.Token), options)
	if err != nil {
		return NormalizedOctoBusServerSpec{}, err
	}
	token = strings.TrimSpace(token)
	if token == redactedOctoBusToken {
		return NormalizedOctoBusServerSpec{}, &ValidationError{
			Path:    path + ".token",
			Message: "redacted token placeholder cannot be used as a credential",
		}
	}
	return NormalizedOctoBusServerSpec{URL: rawURL, Token: token}, nil
}

func validateAgentCapsetReferences(path string, values []string, servers map[string]NormalizedOctoBusServerSpec) error {
	for i, value := range values {
		itemPath := path + "[" + strconv.Itoa(i) + "]"
		declaration, err := capability.ParseCapsetDeclaration(value)
		if err != nil {
			return &ValidationError{Path: itemPath, Message: err.Error()}
		}
		if !declaration.Qualified() {
			continue
		}
		if err := validateStableIdentifier(itemPath, declaration.ServerName, "octobus server name"); err != nil {
			return err
		}
		if _, ok := servers[declaration.ServerName]; !ok {
			return &ValidationError{Path: itemPath, Message: "octobus server " + strconv.Quote(declaration.ServerName) + " is not defined"}
		}
	}
	return nil
}

func orderedOctoBusServers(values map[string]NormalizedOctoBusServerSpec, redactSecrets bool) []orderedOctoBusServerSpec {
	if len(values) == 0 {
		return nil
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	ordered := make([]orderedOctoBusServerSpec, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		token := value.Token
		if redactSecrets && token != "" {
			token = redactedOctoBusToken
		}
		ordered = append(ordered, orderedOctoBusServerSpec{Name: key, URL: value.URL, Token: token})
	}
	return ordered
}

func octoBusMapFromOrdered(values []orderedOctoBusServerSpec) map[string]NormalizedOctoBusServerSpec {
	if len(values) == 0 {
		return nil
	}
	result := make(map[string]NormalizedOctoBusServerSpec, len(values))
	for _, value := range values {
		result[value.Name] = NormalizedOctoBusServerSpec{URL: value.URL, Token: value.Token}
	}
	return result
}
