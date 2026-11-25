package configstore

import (
	"context"
	"fmt"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/projectrunlabel"
	domain "github.com/chaitin/agent-compose/pkg/model"
)

const (
	maxProjectRunLabelKeyLen   = domain.MaxProjectRunLabelKeyBytes
	maxProjectRunLabelValueLen = domain.MaxProjectRunLabelValueBytes
)

// loadProjectRunLabels aggregates the full label set for one run from
// project_run_label. There is no redundant column on project_run, so this
// query is the only way to reconstruct a run's labels for detail-view reads
// (GetProjectRun and the transactional helpers that back RunAgent/GetRun).
// ListProjectRunsByOptions never calls this: labels live on RunDetail, not
// RunSummary, so the high-frequency list/stream path stays free of it.
//
// The client may be bound to a raw transaction, so the transactional run paths
// reuse the same helper without opening a second connection.
func loadProjectRunLabelsByClient(ctx context.Context, client *dao.Client, runID string) (map[string]string, error) {
	entities, err := client.ProjectRunLabel.Query().
		Where(projectrunlabel.IDEQ(runID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("load project run labels %s: %w", runID, err)
	}
	var labels map[string]string
	for _, entity := range entities {
		if labels == nil {
			labels = make(map[string]string, 4)
		}
		labels[entity.Key] = entity.Value
	}
	return labels, nil
}

// insertProjectRunLabels validates and persists labels into
// project_run_label. This is the single write path for run labels: every
// caller, whether it reached here through the CLI's --label flag or a direct
// RunAgent API call, is validated here and only here.
func insertProjectRunLabels(ctx context.Context, client *dao.Client, runID string, labels map[string]string) error {
	normalized, err := domain.NormalizeProjectRunLabels(labels)
	if err != nil {
		return err
	}
	for key, value := range normalized {
		if err := client.ProjectRunLabel.Create().
			SetID(runID).
			SetKey(key).
			SetValue(value).
			OnConflict(entsql.ConflictColumns(projectrunlabel.FieldID, projectrunlabel.FieldKey)).
			Update(func(u *dao.ProjectRunLabelUpsert) {
				u.SetValue(value)
			}).
			Exec(ctx); err != nil {
			return fmt.Errorf("insert project run label %q for run %s: %w", key, runID, err)
		}
	}
	return nil
}
