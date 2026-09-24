package projects

import (
	"fmt"
	"sort"
	"strings"

	"github.com/chaitin/agent-compose/pkg/compose"
	"github.com/chaitin/agent-compose/pkg/llms"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/projectdef"
)

// llmCredentialWarnings reports the LLM credentials a project declares in its
// own environment. Declaring a provider key there is not an error — the daemon
// recognizes the first-party names and proxies them — but it is worth saying out
// loud, because the daemon's connection configuration is the place a credential
// is managed, rotated, and shared deliberately.
//
// The warning states what will actually happen to the value, which differs by
// name: a recognized first-party credential is imported and proxied, so only the
// facade token reaches the sandbox; a recognized credential the daemon cannot
// proxy is still removed from the agent environment, because every provider key
// name is on the passthrough denylist, so the agent never sees it either; a
// credential-looking name the daemon does not recognize is passed through to the
// agent runtime and can be read by anything running there.
func llmCredentialWarnings(spec *projectdef.NormalizedProjectSpec) []ValidationIssue {
	if spec == nil {
		return nil
	}
	var issues []ValidationIssue
	issues = append(issues, credentialIssuesForScope("variables", spec.Variables)...)
	for _, agent := range spec.Agents {
		scope := "agents." + strings.TrimSpace(agent.Name) + ".env"
		issues = append(issues, credentialIssuesForScope(scope, agent.Env)...)
	}
	return issues
}

func credentialIssuesForScope(scope string, values map[string]compose.EnvVarSpec) []ValidationIssue {
	if len(values) == 0 {
		return nil
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	items := make([]domain.SandboxEnvVar, 0, len(names))
	for _, name := range names {
		items = append(items, domain.SandboxEnvVar{Name: name, Value: values[name].Value})
	}
	recognized := make(map[string]llms.DeclaredCredential, len(items))
	for _, credential := range llms.ClassifyDeclaredLLMCredentials(items) {
		recognized[strings.ToUpper(credential.EnvName)] = credential
	}

	var issues []ValidationIssue
	for _, name := range names {
		// An entry with no value contributes nothing to the sandbox: it is a
		// reference the runtime resolves, not a secret in the project file.
		if strings.TrimSpace(values[name].Value) == "" {
			continue
		}
		path := scope + "." + name
		if credential, ok := recognized[strings.ToUpper(name)]; ok {
			issues = append(issues, ValidationIssue{
				Severity: ValidationSeverityWarning,
				Path:     path,
				Message:  declaredCredentialMessage(name, credential),
			})
			continue
		}
		if llms.UnprotectedCredentialEnvName(name) {
			issues = append(issues, ValidationIssue{
				Severity: ValidationSeverityWarning,
				Path:     path,
				Message: fmt.Sprintf(
					"%s is not a provider the daemon recognizes, so its value is passed to the agent runtime and can be read there; configure the provider as a daemon LLM connection if it must be protected",
					name),
			})
		}
	}
	return issues
}

func declaredCredentialMessage(name string, credential llms.DeclaredCredential) string {
	if !credential.Absorbed {
		return fmt.Sprintf(
			"%s is a %s credential the daemon cannot proxy, so the daemon removes it from the agent environment and the agent never sees it; configure this provider as a daemon LLM connection to use it",
			name, credential.Family)
	}
	where := "with no endpoint override, so it points at the vendor's own endpoint"
	if !credential.Official {
		where = fmt.Sprintf("against %s", credential.Endpoint)
	}
	return fmt.Sprintf(
		"%s declares a first-party %s credential %s; the daemon will import it into its LLM connections and proxy the run, so the key stays on the daemon and does not reach the sandbox. Configure the connection on the daemon to manage it explicitly",
		name, credential.Family, where)
}
