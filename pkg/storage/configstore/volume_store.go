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

	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/storeutil"
	"github.com/chaitin/agent-compose/pkg/storedtime"
	"github.com/chaitin/agent-compose/pkg/volumes"

	"github.com/chaitin/agent-compose/pkg/storage/dialect"
)

type volumeStore struct {
	db      *sql.DB
	dialect dialect.Dialect
}

type VolumeListOptions = domain.VolumeListOptions

func (s *volumeStore) CreateVolume(ctx context.Context, item domain.VolumeRecord) (domain.VolumeRecord, error) {
	item.ID = strings.TrimSpace(item.ID)
	if item.ID == "" {
		item.ID = uuid.NewString()
	}
	normalized, err := volumes.NormalizeRecord(item)
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
		if isDuplicateRow(s.dialect, err) {
			return domain.VolumeRecord{}, domain.ResourceError(domain.ErrAlreadyExists, "volume", normalized.Name, fmt.Sprintf("volume %s already exists", normalized.Name), err)
		}
		return domain.VolumeRecord{}, fmt.Errorf("insert volume %s: %w", normalized.Name, err)
	}
	return normalized, nil
}

func (s *volumeStore) UpdateVolume(ctx context.Context, item domain.VolumeRecord) (domain.VolumeRecord, error) {
	normalized, err := volumes.NormalizeRecord(item)
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
	return s.DeleteVolume(ctx, item.ID)
}

func (s *volumeStore) DeleteVolume(ctx context.Context, nameOrID string) error {
	item, err := s.GetVolume(ctx, nameOrID)
	if err != nil {
		return err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin delete volume %s: %w", item.Name, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_volumes WHERE volume_id = ?`, item.ID); err != nil {
		return fmt.Errorf("delete project references for volume %s: %w", item.Name, err)
	}
	result, err := tx.ExecContext(ctx, `DELETE FROM volumes WHERE id = ?`, item.ID)
	if err != nil {
		return fmt.Errorf("delete volume %s: %w", item.Name, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return domain.ResourceError(domain.ErrNotFound, "volume", item.Name, fmt.Sprintf("volume %s not found", item.Name), nil)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit delete volume %s: %w", item.Name, err)
	}
	return nil
}

func (s *volumeStore) UpsertProjectVolume(ctx context.Context, projectID, key string, link domain.ProjectVolumeLink) error {
	projectID = strings.TrimSpace(projectID)
	key = strings.TrimSpace(key)
	volumeID := strings.TrimSpace(link.VolumeID)
	if projectID == "" || key == "" || volumeID == "" {
		return fmt.Errorf("project id, volume key, and volume id are required")
	}
	now := time.Now().UTC().Unix()
	_, err := s.db.ExecContext(ctx, `INSERT INTO project_volumes(project_id, volume_key, volume_id, external, created_at, updated_at)
		VALUES(?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, volume_key) DO UPDATE SET volume_id = excluded.volume_id, external = excluded.external, updated_at = excluded.updated_at`,
		projectID, key, volumeID, BoolToInt(link.External), now, now)
	if err != nil {
		return fmt.Errorf("upsert project volume %s/%s: %w", projectID, key, err)
	}
	return nil
}

func (s *volumeStore) ReplaceProjectVolumes(ctx context.Context, projectID string, links map[string]domain.ProjectVolumeLink) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Errorf("project id is required")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replace project volumes %s: %w", projectID, err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, `DELETE FROM project_volumes WHERE project_id = ?`, projectID); err != nil {
		return fmt.Errorf("clear project volumes %s: %w", projectID, err)
	}
	now := time.Now().UTC().Unix()
	for key, link := range links {
		key = strings.TrimSpace(key)
		volumeID := strings.TrimSpace(link.VolumeID)
		if key == "" || volumeID == "" {
			return fmt.Errorf("project volume key and volume id are required")
		}
		if _, err := tx.ExecContext(ctx, `INSERT INTO project_volumes(project_id, volume_key, volume_id, external, created_at, updated_at)
			VALUES(?, ?, ?, ?, ?, ?)`, projectID, key, volumeID, BoolToInt(link.External), now, now); err != nil {
			return fmt.Errorf("replace project volume %s/%s: %w", projectID, key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace project volumes %s: %w", projectID, err)
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

func (s *volumeStore) RemoveProjectVolumes(ctx context.Context, projectID string) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Errorf("project id is required")
	}
	if _, err := s.db.ExecContext(ctx, `DELETE FROM project_volumes WHERE project_id = ?`, projectID); err != nil {
		return fmt.Errorf("remove project volumes %s: %w", projectID, err)
	}
	return nil
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
	refs, err := s.findProjectVolumeReferences(ctx, volume.ID)
	if err != nil {
		return nil, err
	}
	configRefs, err := s.findVolumeSpecReferences(ctx, volume.Name)
	if err != nil {
		return nil, err
	}
	refs = append(refs, configRefs...)
	return refs, nil
}

// findProjectVolumeReferences lists the project bindings that hold this volume.
func (s *volumeStore) findProjectVolumeReferences(ctx context.Context, volumeID string) (_ []domain.VolumeReference, err error) {
	rows, err := s.db.QueryContext(ctx, `SELECT project_id, volume_key FROM project_volumes WHERE volume_id = ? ORDER BY project_id, volume_key`, volumeID)
	if err != nil {
		return nil, fmt.Errorf("query project volume references: %w", err)
	}
	defer func() { storeutil.ReportClose(rows.Close(), &err, "project volume references") }()
	var refs []domain.VolumeReference
	for rows.Next() {
		var projectID string
		var key string
		if scanErr := rows.Scan(&projectID, &key); scanErr != nil {
			return nil, fmt.Errorf("scan project volume reference: %w", scanErr)
		}
		refs = append(refs, domain.VolumeReference{ResourceType: "project_volume", ResourceID: projectID, Name: key})
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return refs, nil
}

func (s *volumeStore) findVolumeSpecReferences(ctx context.Context, volumeName string) ([]domain.VolumeReference, error) {
	var refs []domain.VolumeReference
	for _, query := range []struct {
		resourceType string
		sql          string
	}{
		{resourceType: "project_agent", sql: `SELECT id, name, COALESCE(spec_json, '{}') FROM project_agent`},
	} {
		queryRefs, err := s.findVolumeSpecReferencesIn(ctx, query.resourceType, query.sql, volumeName)
		if err != nil {
			return nil, err
		}
		refs = append(refs, queryRefs...)
	}
	return refs, nil
}

// findVolumeSpecReferencesIn scans one spec-bearing table for mounts of
// volumeName. It owns its cursor so the caller can loop over several tables
// without holding every cursor open until the walk finishes.
func (s *volumeStore) findVolumeSpecReferencesIn(ctx context.Context, resourceType, query, volumeName string) (_ []domain.VolumeReference, err error) {
	rows, err := s.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("query %s volume references: %w", resourceType, err)
	}
	defer func() { storeutil.ReportClose(rows.Close(), &err, resourceType+" volume references") }()
	var refs []domain.VolumeReference
	for rows.Next() {
		var id string
		var name string
		var raw string
		if scanErr := rows.Scan(&id, &name, &raw); scanErr != nil {
			return nil, fmt.Errorf("scan %s volume reference: %w", resourceType, scanErr)
		}
		if volumeSpecsReferenceSpec(raw, volumeName) {
			refs = append(refs, domain.VolumeReference{ResourceType: resourceType, ResourceID: id, Name: name})
		}
	}
	if rowsErr := rows.Err(); rowsErr != nil {
		return nil, rowsErr
	}
	return refs, nil
}

func volumeSpecsReferenceSpec(raw, volumeName string) bool {
	var spec struct {
		Volumes []domain.VolumeMountSpec `json:"volumes"`
	}
	if err := json.Unmarshal([]byte(strings.TrimSpace(raw)), &spec); err != nil {
		return false
	}
	return volumeSpecsReferenceParts(spec.Volumes, volumeName)
}

func volumeSpecsReferenceParts(specs []domain.VolumeMountSpec, volumeName string) bool {
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
	item.CreatedAt = storedtime.ParseStoredTime(createdAtRaw)
	item.UpdatedAt = storedtime.ParseStoredTime(updatedAtRaw)
	return volumes.NormalizeRecord(item)
}

func encodeStringMapJSON(values map[string]string) (string, error) {
	normalized := volumes.NormalizeStringMap(values)
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
	return volumes.NormalizeStringMap(values), nil
}
