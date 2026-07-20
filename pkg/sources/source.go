package sources

import (
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"
)

const (
	ProviderFile = "file"
	ProviderGit  = "git"
	ProviderHTTP = "http"

	FormatZIP = "zip"
)

var exactEnvReferencePattern = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// Source describes where content is obtained and how it is interpreted.
// Compose authoring types expose these fields inline in their owning object.
type Source struct {
	Provider string `yaml:"provider,omitempty" json:"provider,omitempty"`
	URL      string `yaml:"url,omitempty" json:"url,omitempty"`
	Ref      string `yaml:"ref,omitempty" json:"ref,omitempty"`
	Path     string `yaml:"path,omitempty" json:"path,omitempty"`
	Format   string `yaml:"format,omitempty" json:"format,omitempty"`
	Username string `yaml:"username,omitempty" json:"username,omitempty"`
	Password string `yaml:"password,omitempty" json:"password,omitempty"`
	Token    string `yaml:"token,omitempty" json:"token,omitempty"`
}

func (s Source) Normalized() Source {
	s.Provider = strings.ToLower(strings.TrimSpace(s.Provider))
	s.URL = strings.TrimSpace(s.URL)
	s.Ref = strings.TrimSpace(s.Ref)
	s.Path = strings.TrimSpace(s.Path)
	s.Format = strings.ToLower(strings.TrimSpace(s.Format))
	s.Username = strings.TrimSpace(s.Username)
	s.Password = strings.TrimSpace(s.Password)
	s.Token = strings.TrimSpace(s.Token)
	return s
}

func (s Source) HasContent() bool {
	s = s.Normalized()
	return s.Provider != "" || s.URL != "" || s.Ref != "" || s.Path != "" || s.Format != "" ||
		s.Username != "" || s.Password != "" || s.Token != ""
}

func (s Source) HasAuthentication() bool {
	s = s.Normalized()
	return s.Username != "" || s.Password != "" || s.Token != ""
}

func ValidateSecretReference(field, value string) error {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if !exactEnvReferencePattern.MatchString(value) {
		return fmt.Errorf("%s must be an environment reference like ${NAME}", field)
	}
	return nil
}

func ResolveEnvReference(value string, env map[string]string) string {
	value = strings.TrimSpace(value)
	if !exactEnvReferencePattern.MatchString(value) {
		return value
	}
	name := strings.TrimSuffix(strings.TrimPrefix(value, "${"), "}")
	if env != nil {
		return env[name]
	}
	return os.Getenv(name)
}

// ApplyHTTPAuthentication applies Source authentication without placing
// credentials in the request URL. A token takes precedence over basic auth.
func ApplyHTTPAuthentication(request *http.Request, source Source, env map[string]string) {
	if request == nil {
		return
	}
	username := ResolveEnvReference(source.Username, env)
	password := ResolveEnvReference(source.Password, env)
	token := ResolveEnvReference(source.Token, env)
	if token != "" {
		request.Header.Set("Authorization", "Bearer "+token)
		return
	}
	if username != "" || password != "" {
		request.SetBasicAuth(username, password)
	}
}
