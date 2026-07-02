package capability

import (
	"log/slog"
	"strings"
)

// NormalizeCapsetIDs trims, drops empties, and de-duplicates capset ids while
// preserving order.
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

// BuildGatewaySessionBinding produces the capability env items and tags for a
// session bound to a set of capsets. It never contacts OctoBus, so capability
// setup can never block session/loader creation.
func BuildGatewaySessionBinding(publicTarget string, capsetIDs []string, newToken func() string) ([]SessionEnvVar, []SessionTag) {
	ids := NormalizeCapsetIDs(capsetIDs)
	if len(ids) == 0 {
		return nil, nil
	}
	publicTarget = strings.TrimSpace(publicTarget)
	if publicTarget == "" {
		slog.Warn("capability injection skipped: CAP_GRPC_TARGET not configured", "capsets", ids)
		return nil, nil
	}
	token := ""
	if newToken != nil {
		token = strings.TrimSpace(newToken())
	}
	env := []SessionEnvVar{
		{Name: ProxyTargetEnvName, Value: publicTarget},
		{Name: SessionTokenEnvName, Value: token, Secret: true},
	}
	tags := make([]SessionTag, 0, len(ids))
	for _, id := range ids {
		tags = append(tags, SessionTag{Name: CapsetTagName, Value: id})
	}
	return env, tags
}

// SessionCapabilityCapsets reads the allowed capset set from server-side
// session tags. The guest never sees this list.
func SessionCapabilityCapsets(tags []SessionTag) []string {
	var ids []string
	for _, tag := range tags {
		if tag.Name == CapsetTagName {
			if v := strings.TrimSpace(tag.Value); v != "" {
				ids = append(ids, v)
			}
		}
	}
	return NormalizeCapsetIDs(ids)
}

func SessionEnvValue(env []SessionEnvVar, name string) string {
	for _, item := range env {
		if item.Name == name {
			return strings.TrimSpace(item.Value)
		}
	}
	return ""
}
