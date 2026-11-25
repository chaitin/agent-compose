package configstore

import (
	"context"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/ent/dao/project"
	"github.com/chaitin/agent-compose/ent/dao/projectagent"
	"github.com/chaitin/agent-compose/ent/dao/projectrun"
	"github.com/chaitin/agent-compose/pkg/identity"
	"github.com/chaitin/agent-compose/pkg/resources"
)

func (s *ConfigStore) FindResourceIDs(ctx context.Context, options resources.ResolveOptions) ([]resources.Target, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("config store is required")
	}
	if _, ok := parseResourceIDMatch(options.ID); !ok {
		return nil, nil
	}
	allowed := make(map[resources.Kind]bool, len(options.Kinds))
	for _, kind := range options.Kinds {
		allowed[kind] = true
	}
	var result []resources.Target
	if allowed[resources.KindProject] {
		match, _ := parseResourceIDMatch(options.ID)
		query := s.projectStore.client.Project.Query().Where(project.RemovedAtEQ(0))
		if match.exact {
			query.Where(project.Or(project.IDEQ(match.values[0]), project.IDEQ(match.values[1])))
		} else {
			query.Where(project.Or(
				project.And(project.IDGTE(match.values[0]), project.IDLT(match.values[1])),
				project.And(project.IDGTE(match.values[2]), project.IDLT(match.values[3])),
			))
		}
		entities, err := query.All(ctx)
		if err != nil {
			return nil, fmt.Errorf("find project ids: %w", err)
		}
		for _, entity := range entities {
			result = append(result, resources.Target{Kind: resources.KindProject, ID: entity.ID, ShortID: identity.ShortID(entity.ID), ProjectID: entity.ID, ProjectName: entity.Name})
		}
	}
	if allowed[resources.KindAgent] {
		match, _ := parseResourceIDMatch(options.ID)
		query := s.projectStore.client.ProjectAgent.Query()
		if match.exact {
			query = query.Where(projectagent.Or(
				projectagent.IDEQ(match.values[0]),
				projectagent.IDEQ(match.values[1]),
			))
		} else {
			query = query.Where(projectagent.Or(
				projectagent.And(projectagent.IDGTE(match.values[0]), projectagent.IDLT(match.values[1])),
				projectagent.And(projectagent.IDGTE(match.values[2]), projectagent.IDLT(match.values[3])),
			))
		}
		agents, err := query.All(ctx)
		if err != nil {
			return nil, fmt.Errorf("find agent ids: %w", err)
		}
		projectIDs := make([]string, 0, len(agents))
		seen := make(map[string]struct{}, len(agents))
		for _, agent := range agents {
			if _, ok := seen[agent.ProjectID]; ok {
				continue
			}
			seen[agent.ProjectID] = struct{}{}
			projectIDs = append(projectIDs, agent.ProjectID)
		}
		projectNames := make(map[string]string, len(projectIDs))
		if len(projectIDs) > 0 {
			projects, err := s.projectStore.client.Project.Query().
				Select(project.FieldID, project.FieldName).
				Where(project.IDIn(projectIDs...), project.RemovedAtEQ(0)).
				All(ctx)
			if err != nil {
				return nil, fmt.Errorf("find agent project names: %w", err)
			}
			for _, entity := range projects {
				projectNames[entity.ID] = entity.Name
			}
		}
		for _, agent := range agents {
			projectName, ok := projectNames[agent.ProjectID]
			if !ok {
				continue
			}
			result = append(result, resources.Target{
				Kind:        resources.KindAgent,
				ID:          agent.ID,
				ShortID:     identity.ShortID(agent.ID),
				AgentName:   agent.AgentName,
				ProjectID:   agent.ProjectID,
				ProjectName: projectName,
			})
		}
	}
	if allowed[resources.KindRun] {
		match, _ := parseResourceIDMatch(options.ID)
		query := s.projectStore.client.ProjectRun.Query()
		if match.exact {
			query.Where(projectrun.Or(projectrun.IDEQ(match.values[0]), projectrun.IDEQ(match.values[1])))
		} else {
			query.Where(projectrun.Or(
				projectrun.And(projectrun.IDGTE(match.values[0]), projectrun.IDLT(match.values[1])),
				projectrun.And(projectrun.IDGTE(match.values[2]), projectrun.IDLT(match.values[3])),
			))
		}
		entities, err := query.All(ctx)
		if err != nil {
			return nil, fmt.Errorf("find run ids: %w", err)
		}
		for _, entity := range entities {
			result = append(result, resources.Target{Kind: resources.KindRun, ID: entity.ID, ShortID: identity.ShortID(entity.ID), AgentName: entity.AgentName, ProjectID: entity.ProjectID, ProjectName: entity.ProjectName})
		}
	}
	return result, nil
}

// resourceIDMatch describes the stored identity values a user-supplied prefix
// selects. It mirrors resourceIDClause without emitting SQL.
type resourceIDMatch struct {
	exact  bool
	values []string
}

func parseResourceIDMatch(ref string) (resourceIDMatch, bool) {
	ref = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ref)), identity.Prefix)
	if !identity.IsIDPrefix(ref) {
		return resourceIDMatch{}, false
	}
	if identity.IsID(ref) {
		return resourceIDMatch{exact: true, values: []string{ref, identity.Prefix + ref}}, true
	}
	upper := nextResourceIDPrefix(ref)
	return resourceIDMatch{values: []string{ref, upper, identity.Prefix + ref, identity.Prefix + upper}}, true
}

func nextResourceIDPrefix(prefix string) string {
	value := []byte(prefix)
	for index := len(value) - 1; index >= 0; index-- {
		if value[index] < 'f' {
			if value[index] == '9' {
				value[index] = 'a'
			} else {
				value[index]++
			}
			return string(value[:index+1])
		}
	}
	return "g"
}
