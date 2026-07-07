package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	domain "agent-compose/pkg/model"
)

type volumeStore struct {
	db *sql.DB
}

type VolumeListOptions struct {
	Query     string
	Driver    string
	ProjectID string
}

func (s *volumeStore) ensureVolumeSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS volumes (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL UNIQUE,
			driver TEXT NOT NULL DEFAULT 'local',
			path TEXT NOT NULL DEFAULT '',
			labels_json TEXT NOT NULL DEFAULT '{}',
			options_json TEXT NOT NULL DEFAULT '{}',
			project_id TEXT NOT NULL DEFAULT '',
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
		);`,
		`CREATE INDEX IF NOT EXISTS idx_volumes_driver ON volumes(driver);`,
		`CREATE INDEX IF NOT EXISTS idx_volumes_project ON volumes(project_id);`,
		`CREATE TABLE IF NOT EXISTS project_volumes (
			project_id TEXT NOT NULL,
			volume_key TEXT NOT NULL,
			volume_id TEXT NOT NULL,
			external INTEGER NOT NULL DEFAULT 0,
			created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
			PRIMARY KEY(project_id, volume_key),
			FOREIGN KEY(volume_id) REFERENCES volumes(id) ON DELETE RESTRICT
		);`,
		`CREATE INDEX IF NOT EXISTS idx_project_volumes_volume ON project_volumes(volume_id);`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create volume schema: %w", err)
		}
	}
	return nil
}

func (s *volumeStore) EnsureVolumeSchema(ctx context.Context) error {
	return s.ensureVolumeSchema(ctx)
}

func (s *volumeStore) CreateVolume(ctx context.Context, item domain.VolumeRecord) (domain.VolumeRecord, error) {
	item.ID = strings.TrimSpace(item.ID)
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	normalized, err := domain.NormalizeVolumeRecord(item)
	if err != nil {
		return domain.VolumeRecord{}, err
	}
	labelsJSON, err := encodeStringMapJSON(normalized.Labels)
	if err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("encode volume labels: %w", err)
	}
	optionsJSON, err := encodeStringMapJSON(normalized.Options)
	if err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("encode volume options: %w", err)
	}
	now := time.Now().UTC()
	normalized.CreatedAt = now
	normalized.UpdatedAt = now
	if _, err := s.db.ExecContext(ctx, `INSERT INTO volumes(
		id, name, driver, path, labels_json, options_json, project_id, created_at, updated_at
	) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		normalized.ID, normalized.Name, normalized.Driver, normalized.Path, labelsJSON, optionsJSON, normalized.ProjectID, now.Unix(), now.Unix()); err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "unique") {
			return domain.VolumeRecord{}, domain.ResourceError(domain.ErrAlreadyExists, "volume", normalized.Name, fmt.Sprintf("volume %s already exists", normalized.Name), err)
		}
		return domain.VolumeRecord{}, fmt.Errorf("insert volume %s: %w", normalized.Name, err)
	}
	return normalized, nil
}

func (s *volumeStore) UpdateVolume(ctx context.Context, item domain.VolumeRecord) (domain.VolumeRecord, error) {
	normalized, err := domain.NormalizeVolumeRecord(item)
	if err != nil {
		return domain.VolumeRecord{}, err
	}
	existing, err := s.GetVolume(ctx, normalized.ID)
	if err != nil {
		return domain.VolumeRecord{}, err
	}
	labelsJSON, err := encodeStringMapJSON(normalized.Labels)
	if err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("encode volume labels: %w", err)
	}
	optionsJSON, err := encodeStringMapJSON(normalized.Options)
	if err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("encode volume options: %w", err)
	}
	normalized.CreatedAt = existing.CreatedAt
	normalized.UpdatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE volumes SET
		name = ?, driver = ?, path = ?, labels_json = ?, options_json = ?, project_id = ?, updated_at = ?
		WHERE id = ?`,
		normalized.Name, normalized.Driver, normalized.Path, labelsJSON, optionsJSON, normalized.ProjectID, normalized.UpdatedAt.Unix(), normalized.ID)
	if err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("update volume %s: %w", normalized.ID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.VolumeRecord{}, domain.ResourceError(domain.ErrNotFound, "volume", normalized.ID, fmt.Sprintf("volume %s not found", normalized.ID), nil)
	}
	return normalized, nil
}

func (s *volumeStore) GetVolume(ctx context.Context, nameOrID string) (domain.VolumeRecord, error) {
	item, found, err := s.GetVolumeIfExists(ctx, nameOrID)
	if err != nil {
		return domain.VolumeRecord{}, err
	}
	if !found {
		key := strings.TrimSpace(nameOrID)
		return domain.VolumeRecord{}, domain.ResourceError(domain.ErrNotFound, "volume", key, fmt.Sprintf("volume %s not found", key), sql.ErrNoRows)
	}
	return item, nil
}

func (s *volumeStore) GetVolumeIfExists(ctx context.Context, nameOrID string) (domain.VolumeRecord, bool, error) {
	key := strings.TrimSpace(nameOrID)
	if key == "" {
		return domain.VolumeRecord{}, false, fmt.Errorf("volume name or id is required")
	}
	row := s.db.QueryRowContext(ctx, `SELECT id, name, driver, path, labels_json, options_json, project_id, created_at, updated_at
		FROM volumes WHERE id = ? OR name = ?`, key, key)
	item, err := scanVolume(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.VolumeRecord{}, false, nil
		}
		return domain.VolumeRecord{}, false, err
	}
	return item, true, nil
}

func (s *volumeStore) ListVolumes(ctx context.Context, options VolumeListOptions) ([]domain.VolumeRecord, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT id, name, driver, path, labels_json, options_json, project_id, created_at, updated_at
		FROM volumes ORDER BY name ASC, id ASC`)
	if err != nil {
		return nil, fmt.Errorf("query volumes: %w", err)
	}
	defer func() { _ = rows.Close() }()
	query := strings.ToLower(strings.TrimSpace(options.Query))
	driver := strings.ToLower(strings.TrimSpace(options.Driver))
	projectID := strings.TrimSpace(options.ProjectID)
	var items []domain.VolumeRecord
	for rows.Next() {
		item, err := scanVolume(rows.Scan)
		if err != nil {
			return nil, err
		}
		if query != "" && !strings.Contains(strings.ToLower(item.Name), query) && !strings.Contains(strings.ToLower(item.ID), query) {
			continue
		}
		if driver != "" && item.Driver != driver {
			continue
		}
		if projectID != "" && item.ProjectID != projectID {
			continue
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate volumes: %w", err)
	}
	return items, nil
}

func (s *volumeStore) RemoveVolume(ctx context.Context, nameOrID string) error {
	item, err := s.GetVolume(ctx, nameOrID)
	if err != nil {
		return err
	}
	refs, err := s.FindVolumeConfigReferences(ctx, item.ID)
	if err != nil {
		return err
	}
	if len(refs) > 0 {
		return domain.ResourceError(domain.ErrReferenced, "volume", item.Name, fmt.Sprintf("volume %s is still referenced", item.Name), nil)
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM volumes WHERE id = ?`, item.ID)
	if err != nil {
		return fmt.Errorf("delete volume %s: %w", item.Name, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.ResourceError(domain.ErrNotFound, "volume", item.Name, fmt.Sprintf("volume %s not found", item.Name), nil)
	}
	return nil
}

func (s *volumeStore) UpsertProjectVolume(ctx context.Context, projectID, key, volumeID string, external bool) error {
	projectID = strings.TrimSpace(projectID)
	key = strings.TrimSpace(key)
	volumeID = strings.TrimSpace(volumeID)
	if projectID == "" || key == "" || volumeID == "" {
		return fmt.Errorf("project id, volume key, and volume id are required")
	}
	now := time.Now().UTC().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_volumes(project_id, volume_key, volume_id, external, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, volume_key) DO UPDATE SET volume_id = excluded.volume_id, external = excluded.external, updated_at = excluded.updated_at`,
		projectID, key, volumeID, BoolToInt(external), now, now)
	if err != nil {
		return fmt.Errorf("upsert project volume %s/%s: %w", projectID, key, err)
	}
	return nil
}

func (s *volumeStore) ListProjectVolumes(ctx context.Context, projectID string) (map[string]domain.VolumeRecord, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT v.id, v.name, v.driver, v.path, v.labels_json, v.options_json, v.project_id, v.created_at, v.updated_at, pv.volume_key
		FROM project_volumes pv JOIN volumes v ON v.id = pv.volume_id
		WHERE pv.project_id = ? ORDER BY pv.volume_key ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query project volumes %s: %w", projectID, err)
	}
	defer func() { _ = rows.Close() }()
	items := make(map[string]domain.VolumeRecord)
	for rows.Next() {
		var key string
		item, err := scanVolume(func(dest ...any) error {
			return rows.Scan(append(dest, &key)...)
		})
		if err != nil {
			return nil, err
		}
		items[key] = item
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate project volumes %s: %w", projectID, err)
	}
	return items, nil
}

func (s *volumeStore) FindVolumeConfigReferences(ctx context.Context, volumeID string) ([]domain.VolumeReference, error) {
	volumeID = strings.TrimSpace(volumeID)
	if volumeID == "" {
		return nil, fmt.Errorf("volume id is required")
	}
	volume, err := s.GetVolume(ctx, volumeID)
	if err != nil {
		return nil, err
	}
	var refs []domain.VolumeReference
	projectRows, err := s.db.QueryContext(ctx, `SELECT project_id, volume_key FROM project_volumes WHERE volume_id = ? ORDER BY project_id, volume_key`, volume.ID)
	if err != nil {
		return nil, fmt.Errorf("query project volume references: %w", err)
	}
	for projectRows.Next() {
		var projectID string
		var key string
		if err := projectRows.Scan(&projectID, &key); err != nil {
			_ = projectRows.Close()
			return nil, fmt.Errorf("scan project volume reference: %w", err)
		}
		refs = append(refs, domain.VolumeReference{ResourceType: "project_volume", ResourceID: projectID, Name: key})
	}
	if err := projectRows.Close(); err != nil {
		return nil, err
	}
	if err := projectRows.Err(); err != nil {
		return nil, err
	}
	configRefs, err := s.findVolumeSpecReferences(ctx, volume.Name)
	if err != nil {
		return nil, err
	}
	refs = append(refs, configRefs...)
	return refs, nil
}

func (s *volumeStore) findVolumeSpecReferences(ctx context.Context, volumeName string) ([]domain.VolumeReference, error) {
	var refs []domain.VolumeReference
	for _, query := range []struct {
		resourceType string
		sql          string
	}{
		{resourceType: "agent_definition", sql: `SELECT id, name, volumes_json FROM agent_definition WHERE deleted_at = 0`},
		{resourceType: "loader", sql: `SELECT id, name, volumes_json FROM loader`},
	} {
		rows, err := s.db.QueryContext(ctx, query.sql)
		if err != nil {
			return nil, fmt.Errorf("query %s volume references: %w", query.resourceType, err)
		}
		for rows.Next() {
			var id string
			var name string
			var raw string
			if err := rows.Scan(&id, &name, &raw); err != nil {
				_ = rows.Close()
				return nil, fmt.Errorf("scan %s volume reference: %w", query.resourceType, err)
			}
			if volumeSpecsReference(raw, volumeName) {
				refs = append(refs, domain.VolumeReference{ResourceType: query.resourceType, ResourceID: id, Name: name})
			}
		}
		if err := rows.Close(); err != nil {
			return nil, err
		}
		if err := rows.Err(); err != nil {
			return nil, err
		}
	}
	return refs, nil
}

func volumeSpecsReference(raw, volumeName string) bool {
	var specs []domain.VolumeMountSpec
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &specs); err != nil {
		return false
	}
	for _, spec := range specs {
		if spec.Type == "" || strings.EqualFold(spec.Type, domain.VolumeMountTypeVolume) {
			if strings.TrimSpace(spec.Source) == volumeName {
				return true
			}
		}
	}
	return false
}

func scanVolume(scan func(dest ...any) error) (domain.VolumeRecord, error) {
	var item domain.VolumeRecord
	var labelsJSON string
	var optionsJSON string
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.ID, &item.Name, &item.Driver, &item.Path, &labelsJSON, &optionsJSON, &item.ProjectID, &createdAtRaw, &updatedAtRaw); err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("scan volume: %w", err)
	}
	labels, err := decodeStringMapJSON(labelsJSON)
	if err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("decode volume labels: %w", err)
	}
	options, err := decodeStringMapJSON(optionsJSON)
	if err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("decode volume options: %w", err)
	}
	item.Labels = labels
	item.Options = options
	item.CreatedAt = ParseStoredTime(createdAtRaw)
	item.UpdatedAt = ParseStoredTime(updatedAtRaw)
	return domain.NormalizeVolumeRecord(item)
}

func encodeStringMapJSON(values map[string]string) (string, error) {
	normalized := domain.NormalizeStringMap(values)
	if normalized == nil {
		normalized = map[string]string{}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

func decodeStringMapJSON(raw string) (map[string]string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var values map[string]string
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, err
	}
	return domain.NormalizeStringMap(values), nil
}
