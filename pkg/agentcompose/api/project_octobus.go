package api

import (
	"fmt"
	"slices"
	"strings"

	"agent-compose/pkg/compose"
	agentcomposev2 "agent-compose/proto/agentcompose/v2"
)

func OctoBusServerSpecsToProto(values map[string]compose.NormalizedOctoBusServerSpec) []*agentcomposev2.OctoBusServerSpec {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	slices.Sort(keys)

	items := make([]*agentcomposev2.OctoBusServerSpec, 0, len(keys))
	for _, key := range keys {
		value := values[key]
		items = append(items, &agentcomposev2.OctoBusServerSpec{Name: key, Url: value.URL, Token: value.Token})
	}
	return items
}

func OctoBusServerYAMLMap(servers []*agentcomposev2.OctoBusServerSpec) (map[string]any, []*agentcomposev2.ProjectValidationIssue) {
	values := make(map[string]any, len(servers))
	for i, server := range servers {
		name := strings.TrimSpace(server.GetName())
		if name == "" {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("octobus_servers[%d].name", i), "octobus server name is required")}
		}
		if _, ok := values[name]; ok {
			return nil, []*agentcomposev2.ProjectValidationIssue{ProjectValidationIssue(fmt.Sprintf("octobus_servers[%d].name", i), fmt.Sprintf("duplicate octobus server %q", name))}
		}
		raw := map[string]any{"url": server.GetUrl()}
		if server.GetToken() != "" {
			raw["token"] = server.GetToken()
		}
		values[name] = raw
	}
	return values, nil
}

func redactOctoBusServerSpecs(values []*agentcomposev2.OctoBusServerSpec) {
	for _, value := range values {
		if value != nil && value.GetToken() != "" {
			value.Token = secretRedactedValue
		}
	}
}
