package sources

import (
	"fmt"
	"strings"
)

// ValidateResolvedCredentials rejects unresolved credential references at a
// boundary that accepts caller-supplied values, not access to the host environment.
// Empty credentials are valid for public sources.
func (s Source) ValidateResolvedCredentials() error {
	for _, field := range []struct {
		name  string
		value string
	}{
		{"username", s.Username},
		{"password", s.Password},
		{"token", s.Token},
	} {
		if exactEnvReferencePattern.MatchString(strings.TrimSpace(field.value)) {
			return fmt.Errorf("%s must be resolved by the caller; daemon environment references are not supported", field.name)
		}
	}
	return nil
}
