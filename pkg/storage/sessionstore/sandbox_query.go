package sessionstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	domain "agent-compose/pkg/model"
)

// sandboxWhere builds the WHERE clause and bound arguments for a sandbox list
// query from the list options. It returns an empty clause when no filter is
// set. Every SandboxListOptions filter field is covered, including the keyset
// cursor (BeforeUpdatedAt / BeforeID).
func sandboxWhere(o domain.SandboxListOptions) (string, []any) {
	var conds []string
	var args []any
	contains := func(col, v string) {
		conds = append(conds, "instr("+col+", ?) > 0")
		args = append(args, strings.ToLower(strings.TrimSpace(v)))
	}
	if v := strings.TrimSpace(o.SandboxType); v != "" {
		conds = append(conds, "sandbox_type = ?")
		args = append(args, strings.ToLower(v))
	}
	if v := strings.TrimSpace(o.TriggerSourceQuery); v != "" {
		contains("trigger_source_search", v)
	}
	if v := strings.TrimSpace(o.TitleQuery); v != "" {
		contains("title_search", v)
	}
	if v := strings.TrimSpace(o.WorkspaceQuery); v != "" {
		value := strings.ToLower(v)
		conds = append(conds, "(instr(workspace_path_search, ?) > 0 OR instr(workspace_id_search, ?) > 0 OR instr(nested_workspace_id_search, ?) > 0 OR instr(workspace_name_search, ?) > 0 OR instr(workspace_type_search, ?) > 0)")
		args = append(args, value, value, value, value, value)
	}
	if v := strings.TrimSpace(o.Driver); v != "" {
		conds = append(conds, "driver_search = ?")
		args = append(args, strings.ToLower(v))
	}
	if v := strings.TrimSpace(o.VMStatus); v != "" {
		conds = append(conds, "vm_status_search = ?")
		args = append(args, strings.ToUpper(v))
	}
	if !o.CreatedFrom.IsZero() {
		conds = append(conds, "created_at >= ?")
		args = append(args, sandboxCacheUnixNano(o.CreatedFrom))
	}
	if !o.CreatedTo.IsZero() {
		conds = append(conds, "created_at <= ?")
		args = append(args, sandboxCacheUnixNano(o.CreatedTo))
	}
	if !o.UpdatedFrom.IsZero() {
		conds = append(conds, "updated_at >= ?")
		args = append(args, sandboxCacheUnixNano(o.UpdatedFrom))
	}
	if !o.UpdatedTo.IsZero() {
		conds = append(conds, "updated_at <= ?")
		args = append(args, sandboxCacheUnixNano(o.UpdatedTo))
	}
	if !o.BeforeUpdatedAt.IsZero() {
		conds = append(conds, "(updated_at < ? OR (updated_at = ? AND id < ?))")
		ns := sandboxCacheUnixNano(o.BeforeUpdatedAt)
		args = append(args, ns, ns, o.BeforeID)
	}
	if len(conds) == 0 {
		return "", nil
	}
	return " WHERE " + strings.Join(conds, " AND "), args
}

const sandboxSelectCols = `id, short_id, title, trigger_source, driver, vm_status,
	workspace_path, workspace_id, nested_workspace_id, workspace_name, workspace_type, created_at, updated_at`

// list returns one page of sandboxes ordered by (updated_at DESC, id DESC),
// applying the options' filters, keyset, offset, and limit, plus the total
// count of matching rows. sandboxDir recomputes each result's WorkspacePath to
// match the historical loadSandbox behavior.
func (x *sandboxCache) list(ctx context.Context, o domain.SandboxListOptions, sandboxDir func(string) string) ([]*domain.Sandbox, int, error) {
	where, args := sandboxWhere(o)

	var total int
	if err := x.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sandboxes`+where, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count sandbox listing cache: %w", err)
	}

	offset, limit := domain.NormalizeSandboxListBounds(o.Offset, o.Limit)
	query := `SELECT ` + sandboxSelectCols + ` FROM sandboxes` + where +
		` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`
	rows, err := x.db.QueryContext(ctx, query, append(args, limit, offset)...)
	if err != nil {
		return nil, 0, fmt.Errorf("query sandbox listing cache: %w", err)
	}

	var page []*domain.Sandbox
	for rows.Next() {
		item, err := scanSandboxCacheRow(rows.Scan)
		if err != nil {
			return nil, 0, errors.Join(err, closeSandboxCacheRows(rows))
		}
		item.Summary.WorkspacePath = filepath.Join(sandboxDir(item.Summary.ID), "workspace")
		page = append(page, item)
	}
	if err := rows.Err(); err != nil {
		return nil, 0, errors.Join(fmt.Errorf("iterate sandbox listing cache: %w", err), closeSandboxCacheRows(rows))
	}
	if err := closeSandboxCacheRows(rows); err != nil {
		return nil, 0, err
	}
	return page, total, nil
}

func closeSandboxCacheRows(rows *sql.Rows) error {
	if err := rows.Close(); err != nil {
		return fmt.Errorf("close sandbox listing cache rows: %w", err)
	}
	return nil
}

func scanSandboxCacheRow(scan func(...any) error) (*domain.Sandbox, error) {
	var s domain.SandboxSummary
	var wsID, nestedWSID, wsName, wsType string
	var created, updated int64
	if err := scan(&s.ID, &s.ShortID, &s.Title, &s.TriggerSource, &s.Driver, &s.VMStatus,
		&s.WorkspacePath, &wsID, &nestedWSID, &wsName, &wsType, &created, &updated); err != nil {
		return nil, fmt.Errorf("scan sandbox listing cache row: %w", err)
	}
	s.CreatedAt = sandboxCacheTime(created)
	s.UpdatedAt = sandboxCacheTime(updated)
	return &domain.Sandbox{Summary: s, WorkspaceID: wsID,
		Workspace: &domain.SandboxWorkspace{ID: nestedWSID, Name: wsName, Type: wsType}}, nil
}
