package configstore

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/google/uuid"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/projectagent"
	"github.com/chaitin/agent-compose/ent/dao/projectvolume"
	"github.com/chaitin/agent-compose/ent/dao/volume"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/storage/dialect"
	"github.com/chaitin/agent-compose/pkg/storedtime"
	"github.com/chaitin/agent-compose/pkg/volumes"
)

type volumeStore struct {
	db      *sql.DB
	dialect dialect.Dialect
	client  *dao.Client
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
	if _, err := s.client.Volume.Create().
		SetID(normalized.ID).
		SetName(normalized.Name).
		SetDriver(normalized.Driver).
		SetPath(normalized.Path).
		SetLabelsJSON(labelsJSON).
		SetOptionsJSON(optionsJSON).
		SetProjectID(normalized.ProjectID).
		SetCreatedAt(now.Unix()).
		SetUpdatedAt(now.Unix()).
		Save(ctx); err != nil {
		if s.dialect.IsDuplicateRow(err) {
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
	affected, err := s.client.Volume.Update().
		Where(volume.ID(normalized.ID)).
		SetName(normalized.Name).
		SetDriver(normalized.Driver).
		SetPath(normalized.Path).
		SetLabelsJSON(labelsJSON).
		SetOptionsJSON(optionsJSON).
		SetProjectID(normalized.ProjectID).
		SetUpdatedAt(normalized.UpdatedAt.Unix()).
		Save(ctx)
	if err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("update volume %s: %w", normalized.ID, err)
	}
	if affected == 0 {
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
	entity, err := s.client.Volume.Query().
		Where(volume.Or(volume.ID(key), volume.NameEQ(key))).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return domain.VolumeRecord{}, false, nil
		}
		return domain.VolumeRecord{}, false, err
	}
	item, err := volumeFromEntity(entity)
	if err != nil {
		return domain.VolumeRecord{}, false, err
	}
	return item, true, nil
}

func (s *volumeStore) ListVolumes(ctx context.Context, options VolumeListOptions) ([]domain.VolumeRecord, error) {
	entities, err := s.client.Volume.Query().
		Order(volume.ByName(), volume.ByID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query volumes: %w", err)
	}
	query := strings.ToLower(strings.TrimSpace(options.Query))
	driver := strings.ToLower(strings.TrimSpace(options.Driver))
	projectID := strings.TrimSpace(options.ProjectID)
	items := make([]domain.VolumeRecord, 0, len(entities))
	for _, entity := range entities {
		item, err := volumeFromEntity(entity)
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
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin delete volume %s: %w", item.Name, err)
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if _, err := client.ProjectVolume.Delete().
		Where(projectvolume.VolumeIDEQ(item.ID)).
		Exec(ctx); err != nil {
		return fmt.Errorf("delete project references for volume %s: %w", item.Name, err)
	}
	affected, err := client.Volume.Delete().Where(volume.ID(item.ID)).Exec(ctx)
	if err != nil {
		return fmt.Errorf("delete volume %s: %w", item.Name, err)
	}
	if affected == 0 {
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
	external := BoolToInt(link.External)
	if err := s.client.ProjectVolume.Create().
		SetID(projectID).
		SetVolumeKey(key).
		SetVolumeID(volumeID).
		SetExternal(external).
		SetCreatedAt(now).
		SetUpdatedAt(now).
		OnConflict(entsql.ConflictColumns(projectvolume.FieldID, projectvolume.FieldVolumeKey)).
		Update(func(u *dao.ProjectVolumeUpsert) {
			u.SetVolumeID(volumeID)
			u.SetExternal(external)
			u.SetUpdatedAt(now)
		}).
		Exec(ctx); err != nil {
		return fmt.Errorf("upsert project volume %s/%s: %w", projectID, key, err)
	}
	return nil
}

func (s *volumeStore) ReplaceProjectVolumes(ctx context.Context, projectID string, links map[string]domain.ProjectVolumeLink) error {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return fmt.Errorf("project id is required")
	}
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin replace project volumes %s: %w", projectID, err)
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()
	if _, err := client.ProjectVolume.Delete().Where(projectvolume.IDEQ(projectID)).Exec(ctx); err != nil {
		return fmt.Errorf("clear project volumes %s: %w", projectID, err)
	}
	now := time.Now().UTC().Unix()
	for key, link := range links {
		key = strings.TrimSpace(key)
		volumeID := strings.TrimSpace(link.VolumeID)
		if key == "" || volumeID == "" {
			return fmt.Errorf("project volume key and volume id are required")
		}
		if _, err := client.ProjectVolume.Create().
			SetID(projectID).
			SetVolumeKey(key).
			SetVolumeID(volumeID).
			SetExternal(BoolToInt(link.External)).
			SetCreatedAt(now).
			SetUpdatedAt(now).
			Save(ctx); err != nil {
			return fmt.Errorf("replace project volume %s/%s: %w", projectID, key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit replace project volumes %s: %w", projectID, err)
	}
	return nil
}

// ListProjectVolumes reads the project's volume links together with the volume
// rows they point at. It stays a single join because callers expect one
// consistent snapshot of both tables.
func (s *volumeStore) ListProjectVolumes(ctx context.Context, projectID string) (map[string]domain.VolumeRecord, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	// Kept as SQL: one join returns the link and its volume as a single
	// consistent snapshot; two ent queries could interleave with a delete.
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
	if _, err := s.client.ProjectVolume.Delete().Where(projectvolume.IDEQ(projectID)).Exec(ctx); err != nil {
		return fmt.Errorf("remove project volumes %s: %w", projectID, err)
	}
	return nil
}

func (s *volumeStore) FindVolumeConfigReferences(ctx context.Context, volumeID string) ([]domain.VolumeReference, error) {
	volumeID = strings.TrimSpace(volumeID)
	if volumeID == "" {
		return nil, fmt.Errorf("volume id is required")
	}
	volumeItem, err := s.GetVolume(ctx, volumeID)
	if err != nil {
		return nil, err
	}
	refs, err := s.findProjectVolumeReferences(ctx, volumeItem.ID)
	if err != nil {
		return nil, err
	}
	configRefs, err := s.findVolumeSpecReferences(ctx, volumeItem.Name)
	if err != nil {
		return nil, err
	}
	refs = append(refs, configRefs...)
	return refs, nil
}

// findProjectVolumeReferences lists the project bindings that hold this volume.
func (s *volumeStore) findProjectVolumeReferences(ctx context.Context, volumeID string) ([]domain.VolumeReference, error) {
	entities, err := s.client.ProjectVolume.Query().
		Where(projectvolume.VolumeIDEQ(volumeID)).
		Order(projectvolume.ByID(), projectvolume.ByVolumeKey()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query project volume references: %w", err)
	}
	refs := make([]domain.VolumeReference, 0, len(entities))
	for _, entity := range entities {
		refs = append(refs, domain.VolumeReference{ResourceType: "project_volume", ResourceID: entity.ID, Name: entity.VolumeKey})
	}
	return refs, nil
}

func (s *volumeStore) findVolumeSpecReferences(ctx context.Context, volumeName string) ([]domain.VolumeReference, error) {
	agents, err := s.client.ProjectAgent.Query().
		Select(projectagent.FieldID, projectagent.FieldName, projectagent.FieldSpecJSON).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query project_agent volume references: %w", err)
	}
	refs := make([]domain.VolumeReference, 0)
	for _, agent := range agents {
		if volumeSpecsReferenceSpec(agent.SpecJSON, volumeName) {
			refs = append(refs, domain.VolumeReference{ResourceType: "project_agent", ResourceID: agent.ID, Name: agent.Name})
		}
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

// volumeFromEntity maps one row of volumes to the domain record. The JSON and
// timestamp columns keep their released storage representation.
func volumeFromEntity(entity *dao.Volume) (domain.VolumeRecord, error) {
	labels, err := decodeStringMapJSON(entity.LabelsJSON)
	if err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("decode volume labels: %w", err)
	}
	options, err := decodeStringMapJSON(entity.OptionsJSON)
	if err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("decode volume options: %w", err)
	}
	item := domain.VolumeRecord{
		ID:        entity.ID,
		Name:      entity.Name,
		Driver:    entity.Driver,
		Path:      entity.Path,
		Labels:    labels,
		Options:   options,
		ProjectID: entity.ProjectID,
		CreatedAt: storedtime.ParseStoredTime(entity.CreatedAt),
		UpdatedAt: storedtime.ParseStoredTime(entity.UpdatedAt),
	}
	return volumes.NormalizeRecord(item)
}

// scanVolume reads the volumes columns selected by ListProjectVolumes' join.
func scanVolume(scan func(dest ...any) error) (domain.VolumeRecord, error) {
	var entity dao.Volume
	var createdAt int64
	var updatedAt int64
	if err := scan(&entity.ID, &entity.Name, &entity.Driver, &entity.Path, &entity.LabelsJSON, &entity.OptionsJSON, &entity.ProjectID, &createdAt, &updatedAt); err != nil {
		return domain.VolumeRecord{}, fmt.Errorf("scan volume: %w", err)
	}
	entity.CreatedAt = createdAt
	entity.UpdatedAt = updatedAt
	return volumeFromEntity(&entity)
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
