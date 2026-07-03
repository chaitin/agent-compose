package capabilities

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"

	"github.com/google/uuid"

	"agent-compose/pkg/capproxy"
	domain "agent-compose/pkg/model"
)

const (
	ProxyTargetEnvName  = "CAP_GRPC_TARGET"
	SessionTokenEnvName = "CAP_TOKEN"
	CapsetTagName       = "capset"
)

func NormalizeCapsetIDs(ids []string) []string {
	seen := make(map[string]struct{}, len(ids))
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func EncodeCapsetIDs(ids []string) (string, error) {
	normalized := NormalizeCapsetIDs(ids)
	if normalized == nil {
		normalized = []string{}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode capset ids: %w", err)
	}
	return string(data), nil
}

func DecodeCapsetIDs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return NormalizeCapsetIDs(ids)
}

func BuildGatewaySessionVars(publicTarget string, capsetIDs []string) ([]domain.SessionEnvVar, []domain.SessionTag) {
	ids := NormalizeCapsetIDs(capsetIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	publicTarget = strings.TrimSpace(publicTarget)
	if publicTarget == "" {
		slog.Warn("capability injection skipped: CAP_GRPC_TARGET not configured", "capsets", ids)
		return nil, nil
	}
	env := []domain.SessionEnvVar{
		{Name: ProxyTargetEnvName, Value: publicTarget},
		{Name: SessionTokenEnvName, Value: uuid.NewString(), Secret: true},
	}
	tags := make([]domain.SessionTag, 0, len(ids))
	for _, id := range ids {
		tags = append(tags, domain.SessionTag{Name: CapsetTagName, Value: id})
	}
	return env, tags
}

func GuidePreamble(target string) string {
	target = strings.TrimSpace(target)
	if target == "" {
		return ""
	}
	return fmt.Sprintf(`# Capability Gateway Access

Capabilities are reachable over gRPC through the local capability proxy. To call
any method in the catalog below:

- Endpoint: %s (plaintext HTTP/2 gRPC; also in env CAP_GRPC_TARGET)
- On every call, send metadata `+"`%s: $CAP_TOKEN`"+` (token value is in env CAP_TOKEN)
- Also send the per-method `+"`x-octobus-capset` / `x-octobus-instance`"+`
  metadata shown in the table below
- Schemas can be discovered via gRPC server reflection using the same
  `+"`x-octobus-capset`"+` metadata

`, target, capproxy.SessionTokenMetadata)
}

func SessionRuntimeDir(session *domain.Session) string {
	if session == nil {
		return ""
	}
	workspace := strings.TrimSpace(session.Summary.WorkspacePath)
	if workspace == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(workspace), "runtime")
}

func SessionGuidePath(session *domain.Session) string {
	dir := SessionRuntimeDir(session)
	if dir == "" {
		return ""
	}
	return filepath.Join(dir, "mpi", "catalog.md")
}

func SessionToken(session *domain.Session) string {
	return sessionEnvValue(session, SessionTokenEnvName)
}

func SessionCapsets(session *domain.Session) []string {
	if session == nil {
		return nil
	}
	var ids []string
	for _, tag := range session.Summary.Tags {
		if tag.Name == CapsetTagName {
			if v := strings.TrimSpace(tag.Value); v != "" {
				ids = append(ids, v)
			}
		}
	}
	return NormalizeCapsetIDs(ids)
}

func sessionEnvValue(session *domain.Session, name string) string {
	if session == nil {
		return ""
	}
	for _, item := range session.EnvItems {
		if item.Name == name {
			return strings.TrimSpace(item.Value)
		}
	}
	return ""
}
