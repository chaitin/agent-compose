package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/ent/dao/project"
	"github.com/chaitin/agent-compose/ent/dao/projectrun"
	"github.com/chaitin/agent-compose/pkg/identity"
	"github.com/chaitin/agent-compose/pkg/resources"
)

func (s *ConfigStore) FindResourceIDs(ctx context.Context, options resources.ResolveOptions) ([]resources.Target, error) {
	if s == nil || s.db == nil {
		return nil, fmt.Errorf("config store is required")
	}
	if _, _, ok := resourceIDClause("id", options.ID); !ok {
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
		agentClause, agentArgs, _ := resourceIDClause("pa.id", options.ID)
		// Kept as SQL: joins project_agent with project to resolve the active
		// project's name.
		items, err := queryResourceIDs(ctx, s.db, resourceIDQuery{
			Statement: `SELECT pa.id, pa.agent_name, pa.project_id, p.name FROM project_agent pa JOIN project p ON p.id = pa.project_id WHERE p.removed_at = 0 AND ` + agentClause,
			Args:      agentArgs,
			Build: func(values []string) resources.Target {
				return resources.Target{Kind: resources.KindAgent, ID: values[0], ShortID: identity.ShortID(values[0]), AgentName: values[1], ProjectID: values[2], ProjectName: values[3]}
			},
		})
		if err != nil {
			return nil, fmt.Errorf("find agent ids: %w", err)
		}
		result = append(result, items...)
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

func resourceIDClause(column, ref string) (string, []any, bool) {
	switch column {
	case "id", "pa.id", "run_id":
	default:
		return "", nil, false
	}
	ref = strings.TrimPrefix(strings.ToLower(strings.TrimSpace(ref)), identity.Prefix)
	if !identity.IsIDPrefix(ref) {
		return "", nil, false
	}
	if identity.IsID(ref) {
		return `(` + column + ` = ? OR ` + column + ` = ?)`, []any{ref, identity.Prefix + ref}, true
	}
	upper := nextResourceIDPrefix(ref)
	return `((` + column + ` >= ? AND ` + column + ` < ?) OR (` + column + ` >= ? AND ` + column + ` < ?))`, []any{ref, upper, identity.Prefix + ref, identity.Prefix + upper}, true
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

// resourceIDQuery describes one resource-ID lookup statement and how to build a
// resources.Target from each scanned row.
type resourceIDQuery struct {
	Statement string
	Args      []any
	Build     func([]string) resources.Target
}

func queryResourceIDs(ctx context.Context, db *sql.DB, query resourceIDQuery) ([]resources.Target, error) {
	statement, args, build := query.Statement, query.Args, query.Build
	rows, err := db.QueryContext(ctx, statement, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	columns, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var result []resources.Target
	for rows.Next() {
		values := make([]string, len(columns))
		dest := make([]any, len(values))
		for index := range values {
			dest[index] = &values[index]
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		result = append(result, build(values))
	}
	return result, rows.Err()
}
