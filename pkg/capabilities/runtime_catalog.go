package capabilities

import (
	"context"
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"sort"

	"github.com/chaitin/agent-compose/pkg/capability"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

// ScopedCatalogProvider supplies the same project/user scope as the Markdown guide.
type ScopedCatalogProvider interface {
	CatalogForScope(context.Context, GuideScope, string) (capability.Catalog, error)
}

// RuntimeCatalog is a discovery snapshot, not an authorization credential.
type RuntimeCatalog struct {
	Version     int           `json:"version"`
	SandboxID   string        `json:"sandbox_id"`
	Tools       []RuntimeTool `json:"tools"`
	Unavailable []string      `json:"unavailable_capsets"`
}

// RuntimeTool binds one method to its exact capset declaration and instance.
type RuntimeTool struct {
	ID           string `json:"id"`
	Capset       string `json:"capset"`
	Instance     string `json:"instance"`
	Method       string `json:"method"`
	RequestType  string `json:"request_type"`
	ResponseType string `json:"response_type"`
}

// BuildRuntimeCatalog excludes non-gRPC endpoints and preserves qualified routing.
func BuildRuntimeCatalog(ctx context.Context, provider Provider, scope GuideScope, sandboxID string, ids []string) RuntimeCatalog {
	result := RuntimeCatalog{Version: 1, SandboxID: sandboxID, Tools: []RuntimeTool{}, Unavailable: []string{}}
	scoped, ok := provider.(ScopedCatalogProvider)
	seen := map[string]bool{}
	for _, declaration := range NormalizeCapsetIDs(ids) {
		if !ok {
			result.Unavailable = append(result.Unavailable, declaration)
			continue
		}
		catalog, err := scoped.CatalogForScope(ctx, scope, declaration)
		if err != nil {
			result.Unavailable = append(result.Unavailable, declaration)
			continue
		}
		for _, method := range catalog.Methods {
			tool, valid := runtimeTool(declaration, method)
			if valid && !seen[tool.ID] {
				seen[tool.ID] = true
				result.Tools = append(result.Tools, tool)
			}
		}
	}
	sort.Slice(result.Tools, func(i, j int) bool { return result.Tools[i].ID < result.Tools[j].ID })
	return result
}

func runtimeTool(declaration string, method capability.Method) (RuntimeTool, bool) {
	tool := RuntimeTool{
		Capset: declaration, Instance: method.InstanceID, Method: method.MethodFullName,
		RequestType: method.RequestMessageFullName, ResponseType: method.ResponseMessageFullName,
	}
	if tool.Instance == "" || tool.Method == "" || tool.RequestType == "" || tool.ResponseType == "" {
		return RuntimeTool{}, false
	}
	for _, endpoint := range method.Endpoints {
		if endpoint.Protocol == capability.ProtocolGRPC {
			tool.ID = fmt.Sprintf("%x", sha256.Sum256([]byte(declaration+"\x00"+tool.Instance+"\x00"+tool.Method)))
			return tool, true
		}
	}
	return RuntimeTool{}, false
}

// SandboxCatalogPath locates the structured counterpart of the Markdown guide.
func SandboxCatalogPath(sandbox *domain.Sandbox) string {
	guide := SandboxGuidePath(sandbox)
	if guide == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(guide), "capabilities.json")
}
