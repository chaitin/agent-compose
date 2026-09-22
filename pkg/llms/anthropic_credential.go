package llms

import "strings"

// anthropicCredential is one credential value together with the wire
// authentication semantics it must be presented with.
type anthropicCredential struct {
	apiKey     string
	authHeader string
	authScheme string
}

func anthropicCredentialFromValues(apiKey, authToken, genericKey string) (anthropicCredential, bool) {
	if apiKey = strings.TrimSpace(apiKey); apiKey != "" {
		return anthropicCredential{apiKey: apiKey, authHeader: "x-api-key"}, true
	}
	if authToken = strings.TrimSpace(authToken); authToken != "" {
		return anthropicCredential{apiKey: authToken, authHeader: "Authorization", authScheme: "Bearer"}, true
	}
	if genericKey = strings.TrimSpace(genericKey); genericKey != "" {
		return anthropicCredential{apiKey: genericKey, authHeader: "x-api-key"}, true
	}
	return anthropicCredential{}, false
}
