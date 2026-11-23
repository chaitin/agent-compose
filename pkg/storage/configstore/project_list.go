package configstore

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/chaitin/agent-compose/internal/projects"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/storeutil"

	"github.com/chaitin/agent-compose/pkg/storage/dialect"
)

const (
	defaultProjectListLimit = 50
	maxProjectListLimit     = 500
)

// ListProjects returns one stable project page and the current-revision
// resource counts used by the public project summary. Count and page queries
// share a read transaction so concurrent project updates cannot make the page
// disagree with its reported total.
func (s *projectStore) ListProjects(ctx context.Context, options ProjectListOptions) (ProjectListResult, error) {
	limit, offset := projectListBounds(options)
	where, args := projectListWhere(options, s.dialect)

	tx, err := s.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return ProjectListResult{}, fmt.Errorf("begin project list transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var total int
	if err := tx.QueryRowContext(ctx, `SELECT COUNT(*) FROM project p WHERE `+where, args...).Scan(&total); err != nil {
		return ProjectListResult{}, fmt.Errorf("count projects: %w", err)
	}
	result := ProjectListResult{
		Projects:          []ProjectRecord{},
		CountsByProjectID: make(map[string]domain.ProjectListCounts),
		TotalCount:        total,
		NextOffset:        min(offset, total),
	}
	if offset >= total {
		if err := tx.Commit(); err != nil {
			return ProjectListResult{}, fmt.Errorf("commit empty project list transaction: %w", err)
		}
		return result, nil
	}

	pageArgs := append(append([]any(nil), args...), limit, offset)
	rows, err := tx.QueryContext(ctx, `WITH page AS (
		SELECT p.id, p.name, p.short_id, p.source_path, p.source_json,
			p.current_revision, p.spec_hash, p.created_at, p.updated_at, p.removed_at
		FROM project p
		WHERE `+where+`
		ORDER BY p.updated_at DESC, p.created_at DESC, p.id ASC
		LIMIT ? OFFSET ?
	), agent_counts AS (
		SELECT a.project_id AS id, COUNT(*) AS agent_count
		FROM project_agent a
		JOIN page ON page.id = a.project_id
		WHERE page.current_revision <= 0 OR a.revision = page.current_revision
		GROUP BY a.project_id
	), scheduler_counts AS (
		SELECT s.project_id AS id, COUNT(*) AS scheduler_count
		FROM project_scheduler s
		JOIN page ON page.id = s.project_id
		WHERE page.current_revision <= 0 OR s.revision = page.current_revision
		GROUP BY s.project_id
	)
	SELECT page.id, page.name, page.short_id, page.source_path, page.source_json,
		page.current_revision, page.spec_hash, page.created_at, page.updated_at, page.removed_at,
		COALESCE(agent_counts.agent_count, 0),
		COALESCE(scheduler_counts.scheduler_count, 0)
	FROM page
	LEFT JOIN agent_counts ON agent_counts.id = page.id
	LEFT JOIN scheduler_counts ON scheduler_counts.id = page.id
	ORDER BY page.updated_at DESC, page.created_at DESC, page.id ASC`, pageArgs...)
	if err != nil {
		return ProjectListResult{}, fmt.Errorf("query project page: %w", err)
	}
	// The cursor belongs to tx and must be released before Commit below, so it is
	// consumed in a helper whose deferred Close runs at that point rather than at
	// the end of this function.
	if err := collectProjectListPage(rows, &result); err != nil {
		return ProjectListResult{}, err
	}

	result.NextOffset = offset + len(result.Projects)
	result.HasMore = result.NextOffset < total
	if err := tx.Commit(); err != nil {
		return ProjectListResult{}, fmt.Errorf("commit project list transaction: %w", err)
	}
	return result, nil
}

// collectProjectListPage drains one project page cursor into result. It owns the
// cursor for its whole lifetime so that the deferred Close runs before the
// caller commits the transaction the cursor was opened on.
func collectProjectListPage(rows *sql.Rows, result *ProjectListResult) (err error) {
	defer func() { storeutil.ReportClose(rows.Close(), &err, "project page") }()
	for rows.Next() {
		project, counts, scanErr := scanProjectListRow(rows)
		if scanErr != nil {
			return scanErr
		}
		result.Projects = append(result.Projects, project)
		result.CountsByProjectID[project.ID] = counts
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate project page: %w", err)
	}
	return nil
}

func projectListBounds(options ProjectListOptions) (limit, offset int) {
	limit = options.Limit
	if limit <= 0 {
		limit = defaultProjectListLimit
	}
	if limit > maxProjectListLimit {
		limit = maxProjectListLimit
	}
	offset = max(options.Offset, 0)
	return limit, offset
}

func projectListWhere(options ProjectListOptions, d dialect.Dialect) (string, []any) {
	clauses := make([]string, 0, 2)
	args := make([]any, 0, 3)
	if !options.IncludeRemoved {
		clauses = append(clauses, "p.removed_at = 0")
	}
	if query := strings.ToLower(strings.TrimSpace(options.Query)); query != "" {
		d = dialect.OrSQLite(d)
		clauses = append(clauses, "("+d.Contains("lower(p.id)")+" OR "+d.Contains("lower(p.name)")+" OR "+d.Contains("lower(p.source_path)")+")")
		args = append(args, query, query, query)
	}
	if len(clauses) == 0 {
		return "1 = 1", args
	}
	return strings.Join(clauses, " AND "), args
}

type projectListRows interface {
	Scan(...any) error
}

func scanProjectListRow(row projectListRows) (ProjectRecord, domain.ProjectListCounts, error) {
	var agentCount int64
	var schedulerCount int64
	project, err := projects.ScanProject(func(dest ...any) error {
		dest = append(dest, &agentCount, &schedulerCount)
		return row.Scan(dest...)
	})
	if err != nil {
		return ProjectRecord{}, domain.ProjectListCounts{}, err
	}
	return project, domain.ProjectListCounts{
		AgentCount:     uint32(agentCount),
		SchedulerCount: uint32(schedulerCount),
	}, nil
}
