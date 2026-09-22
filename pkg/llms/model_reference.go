package llms

import (
	"fmt"
	"strings"

	domain "github.com/chaitin/agent-compose/pkg/model"
)

// SplitModelReference parses the legacy agent-facing model selection shared by
// the old facade path.
//
// Deprecated: the call chain no longer reads structure out of a model string.
// Catalog.SelectModel treats a model as opaque, and Dialect.GuestModel is the
// only place a <provider>/<model> reference is composed. This helper survives
// only for the runtime proxy's compatibility resolution and goes away with it.
func SplitModelReference(value string) (string, string, error) {
	value = strings.TrimSpace(value)
	if value == "" || !strings.Contains(value, "/") {
		return "", value, nil
	}
	providerID, model, ok := SplitProviderModelReference(value)
	if !ok {
		return "", "", domain.ClassifyError(domain.ErrInvalidArgument, fmt.Sprintf(
			"llm model reference %q must not leave either side of <llm-provider-id>/<model-name> empty", value), nil)
	}
	return providerID, model, nil
}
