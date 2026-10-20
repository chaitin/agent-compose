package execution

import (
	"errors"
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
	agentcomposev2 "github.com/chaitin/agent-compose/proto/agentcompose/v2"
)

// ErrSecretCommandEnvUnsupported identifies command requests that try to use
// the public secret marker without a command-scoped secret transport.
var ErrSecretCommandEnvUnsupported = errors.New("secret command environment is unsupported")

const secretCommandEnvRejection = "secret_command_env_unsupported"

// ValidateCommandEnv rejects secret-marked request environment variables
// before command execution can persist or forward their values. Project and
// agent configuration env remains unchanged; this guard only covers the
// per-command request env accepted by RunAgent and Exec.
func ValidateCommandEnv(items []*agentcomposev2.EnvVarSpec) error {
	for _, item := range items {
		if item == nil || !item.GetSecret() {
			continue
		}
		name := strings.TrimSpace(item.GetName())
		if name == "" {
			name = "<unnamed>"
		}
		return domain.ClassifyError(
			domain.ErrInvalidArgument,
			fmt.Sprintf("%s: command environment variable %q uses secret=true, which is unsupported", secretCommandEnvRejection, name),
			ErrSecretCommandEnvUnsupported,
		)
	}
	return nil
}

// ValidateSandboxCommandEnv rejects secret-marked sandbox environment
// variables supplied to scheduler commands. Daemon-managed runtime env (for
// example a facade token) is validated separately and is not part of this
// caller-controlled request.
func ValidateSandboxCommandEnv(items []domain.SandboxEnvVar) error {
	for _, item := range items {
		if !item.Secret {
			continue
		}
		name := strings.TrimSpace(item.Name)
		if name == "" {
			name = "<unnamed>"
		}
		return domain.ClassifyError(
			domain.ErrInvalidArgument,
			fmt.Sprintf("%s: command sandbox environment variable %q uses secret=true, which is unsupported", secretCommandEnvRejection, name),
			ErrSecretCommandEnvUnsupported,
		)
	}
	return nil
}
