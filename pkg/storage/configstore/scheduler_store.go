package configstore

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	entsql "entgo.io/ent/dialect/sql"

	"github.com/chaitin/agent-compose/ent/dao"
	"github.com/chaitin/agent-compose/ent/dao/projectscheduler"
	"github.com/chaitin/agent-compose/ent/dao/schedulerevent"
	"github.com/chaitin/agent-compose/ent/dao/schedulerrun"
	"github.com/chaitin/agent-compose/ent/dao/schedulersandboxbinding"
	"github.com/chaitin/agent-compose/ent/dao/schedulerstate"
	"github.com/chaitin/agent-compose/ent/dao/schedulertrigger"
	domain "github.com/chaitin/agent-compose/pkg/model"
	"github.com/chaitin/agent-compose/pkg/schedulers"
	"github.com/chaitin/agent-compose/pkg/storedtime"
)

// schedulerStore owns scheduler execution state, triggers, runs, and events.
type schedulerStore struct {
	db     *sql.DB
	client *dao.Client
}

func (s *schedulerStore) ListSchedulerSummaries(ctx context.Context) ([]domain.SchedulerSummary, error) {
	entities, err := s.client.ProjectScheduler.Query().
		Select(projectscheduler.FieldID).
		Order(dao.Desc(projectscheduler.FieldUpdatedAt, projectscheduler.FieldCreatedAt, projectscheduler.FieldID)).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query schedulers: %w", err)
	}

	items := make([]domain.SchedulerSummary, 0, len(entities))
	for _, entity := range entities {
		item, err := s.GetScheduler(ctx, entity.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, item.Summary)
	}
	return items, nil
}

func (s *schedulerStore) GetScheduler(ctx context.Context, schedulerID string) (Scheduler, error) {
	return s.loadSchedulerDefinition(ctx, schedulerID)
}

func (s *schedulerStore) ListSchedulers(ctx context.Context) ([]Scheduler, error) {
	summaries, err := s.ListSchedulerSummaries(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]Scheduler, 0, len(summaries))
	for _, summary := range summaries {
		item, err := s.GetScheduler(ctx, summary.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

// Kept as SQL: aggregate counts and MAX across scheduler_trigger, scheduler_run,
// and a join with scheduler_event; ent cannot express this as one consistent read.
func (s *schedulerStore) hydrateSchedulerSummaryCounts(ctx context.Context, summary *domain.SchedulerSummary) error {
	if summary == nil || strings.TrimSpace(summary.ID) == "" {
		return nil
	}
	row := s.db.QueryRowContext(ctx, `SELECT
		(SELECT COUNT(*) FROM scheduler_trigger WHERE scheduler_id = ?),
		(SELECT COUNT(*) FROM scheduler_run WHERE scheduler_id = ? AND trigger_id <> ''),
		(SELECT COUNT(*) FROM scheduler_event e JOIN scheduler_run r ON r.scheduler_id = e.scheduler_id AND r.run_id = e.scheduler_run_id WHERE e.scheduler_id = ? AND r.trigger_id <> ''),
		(SELECT MAX(started_at) FROM scheduler_run WHERE scheduler_id = ? AND trigger_id <> '')`, summary.ID, summary.ID, summary.ID, summary.ID)
	var triggerCount int
	var runCount int
	var eventCount int
	var latestRunAtRaw any
	if err := row.Scan(&triggerCount, &runCount, &eventCount, &latestRunAtRaw); err != nil {
		return fmt.Errorf("load scheduler summary counts: %w", err)
	}
	summary.TriggerCount = triggerCount
	summary.RunCount = runCount
	summary.EventCount = eventCount
	summary.LatestRunAt = storedtime.ParseStoredTime(latestRunAtRaw)
	return nil
}

func (s *schedulerStore) ReplaceSchedulerTriggers(ctx context.Context, schedulerID string, triggers []domain.SchedulerTrigger) ([]domain.SchedulerTrigger, error) {
	schedulerID = strings.TrimSpace(schedulerID)
	if schedulerID == "" {
		return nil, fmt.Errorf("scheduler id is required")
	}
	existing, err := s.listSchedulerTriggers(ctx, schedulerID)
	if err != nil {
		return nil, err
	}
	existingByID := make(map[string]domain.SchedulerTrigger, len(existing))
	for _, item := range existing {
		existingByID[item.ID] = item
	}

	normalized := make([]domain.SchedulerTrigger, 0, len(triggers))
	seen := make(map[string]struct{}, len(triggers))
	now := time.Now().UTC()
	for _, trigger := range triggers {
		current, err := schedulers.NormalizeSchedulerTrigger(schedulerID, trigger)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[current.ID]; ok {
			return nil, fmt.Errorf("duplicate scheduler trigger id %q", current.ID)
		}
		seen[current.ID] = struct{}{}
		sameSchedule := false
		if previous, ok := existingByID[current.ID]; ok {
			current.Enabled = previous.Enabled
			current.LastFiredAt = previous.LastFiredAt
			if current.Kind == previous.Kind && current.Topic == previous.Topic && current.IntervalMs == previous.IntervalMs && current.SpecJSON == previous.SpecJSON {
				current.NextFireAt = previous.NextFireAt
				sameSchedule = true
			}
		}
		if !current.Enabled {
			current.NextFireAt = time.Time{}
		} else {
			switch current.Kind {
			case domain.SchedulerTriggerKindInterval:
				if current.NextFireAt.IsZero() {
					current.NextFireAt = schedulers.TriggerScheduledAt(now, current.IntervalMs)
				}
			case domain.SchedulerTriggerKindTimeout:
				if !sameSchedule {
					current.NextFireAt = schedulers.TriggerScheduledAt(now, current.IntervalMs)
				}
			case domain.SchedulerTriggerKindCron:
				if !sameSchedule || current.NextFireAt.IsZero() {
					nextFireAt, err := schedulers.SchedulerTriggerNextFireAt(now, current, false)
					if err != nil {
						return nil, err
					}
					current.NextFireAt = nextFireAt
				}
			}
		}
		normalized = append(normalized, current)
	}

	tx, err := s.client.Tx(ctx)
	if err != nil {
		return nil, fmt.Errorf("begin scheduler trigger tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()

	if _, err := client.SchedulerTrigger.Delete().
		Where(schedulertrigger.IDEQ(schedulerID)).
		Exec(ctx); err != nil {
		return nil, fmt.Errorf("reset scheduler triggers: %w", err)
	}
	for _, trigger := range normalized {
		if _, err := client.SchedulerTrigger.Create().
			SetID(trigger.SchedulerID).
			SetTriggerID(trigger.ID).
			SetKind(trigger.Kind).
			SetTopic(trigger.Topic).
			SetIntervalMs(trigger.IntervalMs).
			SetEnabled(BoolToInt(trigger.Enabled)).
			SetAutoID(BoolToInt(trigger.AutoID)).
			SetSpecJSON(trigger.SpecJSON).
			SetNextFireAt(domain.NonZeroTimeUnixMilli(trigger.NextFireAt)).
			SetLastFiredAt(domain.NonZeroTimeUnixMilli(trigger.LastFiredAt)).
			Save(ctx); err != nil {
			return nil, fmt.Errorf("insert scheduler trigger %s/%s: %w", schedulerID, trigger.ID, err)
		}
	}
	if _, err := client.ProjectScheduler.Update().
		Where(projectscheduler.IDEQ(schedulerID)).
		SetUpdatedAt(time.Now().UTC().Unix()).
		Save(ctx); err != nil {
		return nil, fmt.Errorf("touch scheduler after trigger replace: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit scheduler trigger tx: %w", err)
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Kind != normalized[j].Kind {
			return normalized[i].Kind < normalized[j].Kind
		}
		return normalized[i].ID < normalized[j].ID
	})
	return normalized, nil
}

func (s *schedulerStore) listSchedulerTriggers(ctx context.Context, schedulerID string) ([]domain.SchedulerTrigger, error) {
	entities, err := s.client.SchedulerTrigger.Query().
		Where(schedulertrigger.IDEQ(schedulerID)).
		Order(schedulertrigger.ByKind(), schedulertrigger.ByTriggerID()).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query scheduler triggers: %w", err)
	}

	items := make([]domain.SchedulerTrigger, 0, len(entities))
	for _, entity := range entities {
		items = append(items, schedulerTriggerFromEntity(entity))
	}
	return items, nil
}

func (s *schedulerStore) SetSchedulerEnabled(ctx context.Context, schedulerID string, enabled bool) error {
	schedulerID = strings.TrimSpace(schedulerID)
	if schedulerID == "" {
		return fmt.Errorf("scheduler id is required")
	}
	var (
		triggers []domain.SchedulerTrigger
		err      error
	)
	if enabled {
		triggers, err = s.listSchedulerTriggers(ctx, schedulerID)
		if err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	tx, err := s.client.Tx(ctx)
	if err != nil {
		return fmt.Errorf("begin scheduler enable tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	client := tx.Client()

	affected, err := client.ProjectScheduler.Update().
		Where(projectscheduler.IDEQ(schedulerID)).
		SetEnabled(BoolToInt(enabled)).
		SetUpdatedAt(now.Unix()).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("update scheduler enabled state: %w", err)
	}
	if affected == 0 {
		return domain.ResourceError(domain.ErrNotFound, "scheduler", schedulerID, fmt.Sprintf("scheduler %s not found", schedulerID), nil)
	}
	if enabled {
		for _, trigger := range triggers {
			if !trigger.Enabled || !schedulers.TriggerUsesSchedule(trigger.Kind) {
				continue
			}
			nextFireAt, err := schedulers.SchedulerTriggerNextFireAt(now, trigger, false)
			if err != nil {
				return fmt.Errorf("schedule scheduler trigger %s/%s: %w", schedulerID, trigger.ID, err)
			}
			if _, err := client.SchedulerTrigger.Update().
				Where(schedulertrigger.IDEQ(schedulerID), schedulertrigger.TriggerIDEQ(trigger.ID)).
				SetNextFireAt(domain.NonZeroTimeUnixMilli(nextFireAt)).
				Save(ctx); err != nil {
				return fmt.Errorf("schedule scheduler trigger %s/%s: %w", schedulerID, trigger.ID, err)
			}
		}
	} else {
		if _, err := client.SchedulerTrigger.Update().
			Where(
				schedulertrigger.IDEQ(schedulerID),
				schedulertrigger.KindIn(domain.SchedulerTriggerKindInterval, domain.SchedulerTriggerKindTimeout, domain.SchedulerTriggerKindCron),
			).
			SetNextFireAt(0).
			Save(ctx); err != nil {
			return fmt.Errorf("pause scheduler triggers: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit scheduler enable tx: %w", err)
	}
	return nil
}

func (s *schedulerStore) SetSchedulerTriggerEnabled(ctx context.Context, schedulerID, triggerID string, enabled bool) error {
	schedulerID = strings.TrimSpace(schedulerID)
	triggerID = strings.TrimSpace(triggerID)
	if schedulerID == "" || triggerID == "" {
		return fmt.Errorf("scheduler trigger id is required")
	}
	entity, err := s.client.SchedulerTrigger.Query().
		Where(schedulertrigger.IDEQ(schedulerID), schedulertrigger.TriggerIDEQ(triggerID)).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			id := schedulerID + "/" + triggerID
			return domain.ResourceError(domain.ErrNotFound, "scheduler trigger", id, fmt.Sprintf("scheduler trigger %s not found", id), sql.ErrNoRows)
		}
		return err
	}
	trigger := schedulerTriggerFromEntity(entity)
	nextFireAt := int64(0)
	if enabled && schedulers.TriggerUsesSchedule(trigger.Kind) {
		scheduledAt, err := schedulers.SchedulerTriggerNextFireAt(time.Now().UTC(), trigger, false)
		if err != nil {
			return fmt.Errorf("schedule scheduler trigger %s/%s: %w", schedulerID, triggerID, err)
		}
		nextFireAt = domain.NonZeroTimeUnixMilli(scheduledAt)
	}
	affected, err := s.client.SchedulerTrigger.Update().
		Where(schedulertrigger.IDEQ(schedulerID), schedulertrigger.TriggerIDEQ(triggerID)).
		SetEnabled(BoolToInt(enabled)).
		SetNextFireAt(nextFireAt).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("update scheduler trigger enabled state: %w", err)
	}
	if affected == 0 {
		id := schedulerID + "/" + triggerID
		return domain.ResourceError(domain.ErrNotFound, "scheduler trigger", id, fmt.Sprintf("scheduler trigger %s not found", id), nil)
	}
	_, _ = s.client.ProjectScheduler.Update().
		Where(projectscheduler.IDEQ(schedulerID)).
		SetUpdatedAt(time.Now().UTC().Unix()).
		Save(ctx)
	return nil
}

func (s *schedulerStore) UpdateSchedulerLastError(ctx context.Context, schedulerID, lastError string) error {
	schedulerID = strings.TrimSpace(schedulerID)
	if schedulerID == "" {
		return fmt.Errorf("scheduler id is required")
	}
	if _, err := s.client.ProjectScheduler.Update().
		Where(projectscheduler.IDEQ(schedulerID)).
		SetLastError(strings.TrimSpace(lastError)).
		SetUpdatedAt(time.Now().UTC().Unix()).
		Save(ctx); err != nil {
		return fmt.Errorf("update scheduler last error: %w", err)
	}
	return nil
}

func (s *schedulerStore) MarkSchedulerTriggerFired(ctx context.Context, schedulerID, triggerID string, lastFiredAt, nextFireAt time.Time) error {
	if _, err := s.client.SchedulerTrigger.Update().
		Where(schedulertrigger.IDEQ(strings.TrimSpace(schedulerID)), schedulertrigger.TriggerIDEQ(strings.TrimSpace(triggerID))).
		SetLastFiredAt(domain.NonZeroTimeUnixMilli(lastFiredAt)).
		SetNextFireAt(domain.NonZeroTimeUnixMilli(nextFireAt)).
		Save(ctx); err != nil {
		return fmt.Errorf("update scheduler trigger fire state: %w", err)
	}
	return nil
}

func (s *schedulerStore) SetSchedulerTriggerNextFireAt(ctx context.Context, schedulerID, triggerID string, nextFireAt time.Time) error {
	schedulerID = strings.TrimSpace(schedulerID)
	triggerID = strings.TrimSpace(triggerID)
	if schedulerID == "" || triggerID == "" {
		return fmt.Errorf("scheduler trigger id is required")
	}
	affected, err := s.client.SchedulerTrigger.Update().
		Where(schedulertrigger.IDEQ(schedulerID), schedulertrigger.TriggerIDEQ(triggerID)).
		SetNextFireAt(domain.NonZeroTimeUnixMilli(nextFireAt)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("update scheduler trigger next fire time: %w", err)
	}
	if affected == 0 {
		id := schedulerID + "/" + triggerID
		return domain.ResourceError(domain.ErrNotFound, "scheduler trigger", id, fmt.Sprintf("scheduler trigger %s not found", id), nil)
	}
	return nil
}

func (s *schedulerStore) CreateSchedulerRun(ctx context.Context, run domain.SchedulerRunSummary) error {
	if err := validateHistoryArtifactPaths(&schedulerRunOwner{SchedulerID: run.SchedulerID, RunID: run.ID}, run.ArtifactsDir); err != nil {
		return err
	}
	if _, err := s.client.SchedulerRun.Create().
		SetID(strings.TrimSpace(run.ID)).
		SetSchedulerID(strings.TrimSpace(run.SchedulerID)).
		SetTriggerID(strings.TrimSpace(run.TriggerID)).
		SetTriggerKind(strings.TrimSpace(run.TriggerKind)).
		SetTriggerSource(strings.TrimSpace(run.TriggerSource)).
		SetStatus(schedulers.NormalizeRunStatus(run.Status)).
		SetStartedAt(run.StartedAt.UTC().UnixMilli()).
		SetCompletedAt(domain.NonZeroTimeUnixMilli(run.CompletedAt)).
		SetDurationMs(run.DurationMs).
		SetError(run.Error).
		SetResultJSON(run.ResultJSON).
		SetPayloadJSON(run.PayloadJSON).
		SetSourceScriptSha256(run.SourceScriptHash).
		SetArtifactsDir(strings.TrimSpace(run.ArtifactsDir)).
		Save(ctx); err != nil {
		return fmt.Errorf("insert scheduler run %s/%s: %w", run.SchedulerID, run.ID, err)
	}
	return nil
}

func (s *schedulerStore) UpdateSchedulerRun(ctx context.Context, run domain.SchedulerRunSummary) error {
	if err := validateHistoryArtifactPaths(&schedulerRunOwner{SchedulerID: run.SchedulerID, RunID: run.ID}, run.ArtifactsDir); err != nil {
		return err
	}
	affected, err := s.client.SchedulerRun.Update().
		Where(
			schedulerrun.SchedulerIDEQ(strings.TrimSpace(run.SchedulerID)),
			schedulerrun.IDEQ(strings.TrimSpace(run.ID)),
		).
		SetTriggerID(strings.TrimSpace(run.TriggerID)).
		SetTriggerKind(strings.TrimSpace(run.TriggerKind)).
		SetTriggerSource(strings.TrimSpace(run.TriggerSource)).
		SetStatus(schedulers.NormalizeRunStatus(run.Status)).
		SetStartedAt(run.StartedAt.UTC().UnixMilli()).
		SetCompletedAt(domain.NonZeroTimeUnixMilli(run.CompletedAt)).
		SetDurationMs(run.DurationMs).
		SetError(run.Error).
		SetResultJSON(run.ResultJSON).
		SetPayloadJSON(run.PayloadJSON).
		SetSourceScriptSha256(run.SourceScriptHash).
		SetArtifactsDir(strings.TrimSpace(run.ArtifactsDir)).
		Save(ctx)
	if err != nil {
		return fmt.Errorf("update scheduler run %s/%s: %w", run.SchedulerID, run.ID, err)
	}
	if affected == 0 {
		id := strings.TrimSpace(run.SchedulerID) + "/" + strings.TrimSpace(run.ID)
		return domain.ResourceError(domain.ErrNotFound, "scheduler run", id, fmt.Sprintf("scheduler run %s not found", id), nil)
	}
	return nil
}

func (s *schedulerStore) GetSchedulerRun(ctx context.Context, schedulerID, runID string) (domain.SchedulerRunSummary, error) {
	entity, err := s.client.SchedulerRun.Query().
		Where(
			schedulerrun.SchedulerIDEQ(strings.TrimSpace(schedulerID)),
			schedulerrun.IDEQ(strings.TrimSpace(runID)),
		).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			id := strings.TrimSpace(schedulerID) + "/" + strings.TrimSpace(runID)
			return domain.SchedulerRunSummary{}, domain.ResourceError(domain.ErrNotFound, "scheduler run", id, fmt.Sprintf("scheduler run %s not found", id), sql.ErrNoRows)
		}
		return domain.SchedulerRunSummary{}, err
	}
	return schedulerRunFromEntity(entity), nil
}

func (s *schedulerStore) ListSchedulerRuns(ctx context.Context, schedulerID string, limit int) ([]domain.SchedulerRunSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	entities, err := s.client.SchedulerRun.Query().
		Where(schedulerrun.SchedulerIDEQ(strings.TrimSpace(schedulerID))).
		Order(dao.Desc(schedulerrun.FieldStartedAt, schedulerrun.FieldID)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query scheduler runs: %w", err)
	}

	items := make([]domain.SchedulerRunSummary, 0, len(entities))
	for _, entity := range entities {
		items = append(items, schedulerRunFromEntity(entity))
	}
	return items, nil
}

func (s *schedulerStore) ListRecentSchedulerRuns(ctx context.Context, limit int) ([]domain.SchedulerRunSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	entities, err := s.client.SchedulerRun.Query().
		Order(dao.Desc(schedulerrun.FieldStartedAt, schedulerrun.FieldSchedulerID, schedulerrun.FieldID)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query recent scheduler runs: %w", err)
	}

	items := make([]domain.SchedulerRunSummary, 0, len(entities))
	for _, entity := range entities {
		items = append(items, schedulerRunFromEntity(entity))
	}
	return items, nil
}

func (s *schedulerStore) AddSchedulerEvent(ctx context.Context, event domain.SchedulerEvent) error {
	if _, err := s.client.SchedulerEvent.Create().
		SetID(strings.TrimSpace(event.SchedulerID)).
		SetEventID(strings.TrimSpace(event.ID)).
		SetSchedulerRunID(strings.TrimSpace(event.RunID)).
		SetTriggerID(strings.TrimSpace(event.TriggerID)).
		SetType(strings.TrimSpace(event.Type)).
		SetLevel(strings.TrimSpace(event.Level)).
		SetMessage(strings.TrimSpace(event.Message)).
		SetPayloadJSON(strings.TrimSpace(event.PayloadJSON)).
		SetLinkedSandboxID(strings.TrimSpace(event.LinkedSandboxID)).
		SetLinkedCellID(strings.TrimSpace(event.LinkedCellID)).
		SetLinkedAgentThreadID(strings.TrimSpace(event.LinkedAgentThreadID)).
		SetCreatedAt(event.CreatedAt.UTC().UnixMilli()).
		Save(ctx); err != nil {
		return fmt.Errorf("insert scheduler event %s/%s: %w", event.SchedulerID, event.ID, err)
	}
	return nil
}

func (s *schedulerStore) ListSchedulerEvents(ctx context.Context, schedulerID string, limit int) ([]domain.SchedulerEvent, error) {
	return s.ListSchedulerEventsBefore(ctx, SchedulerEventsBeforeCursor{SchedulerID: schedulerID, Limit: limit})
}

// SchedulerEventsBeforeCursor paginates scheduler events strictly before the given
// (created_at, event_id) position; a zero Before lists from the most recent event.
type SchedulerEventsBeforeCursor struct {
	SchedulerID string
	Before      time.Time
	BeforeID    string
	Limit       int
}

func (s *schedulerStore) ListSchedulerEventsBefore(ctx context.Context, cursor SchedulerEventsBeforeCursor) ([]domain.SchedulerEvent, error) {
	schedulerID, before, beforeID, limit := cursor.SchedulerID, cursor.Before, cursor.BeforeID, cursor.Limit
	if limit <= 0 {
		limit = 100
	}
	query := s.client.SchedulerEvent.Query().
		Where(schedulerevent.IDEQ(strings.TrimSpace(schedulerID)))
	if !before.IsZero() {
		millis := before.UTC().UnixMilli()
		query = query.Where(schedulerevent.Or(
			schedulerevent.CreatedAtLT(millis),
			schedulerevent.And(schedulerevent.CreatedAtEQ(millis), schedulerevent.EventIDLT(strings.TrimSpace(beforeID))),
		))
	}
	entities, err := query.
		Order(dao.Desc(schedulerevent.FieldCreatedAt, schedulerevent.FieldEventID)).
		Limit(limit).
		All(ctx)
	if err != nil {
		return nil, fmt.Errorf("query scheduler events: %w", err)
	}

	items := make([]domain.SchedulerEvent, 0, len(entities))
	for _, entity := range entities {
		items = append(items, schedulerEventFromEntity(entity))
	}
	return items, nil
}

func (s *schedulerStore) GetSchedulerState(ctx context.Context, schedulerID, key string) (string, bool, error) {
	entity, err := s.client.SchedulerState.Query().
		Where(
			schedulerstate.IDEQ(strings.TrimSpace(schedulerID)),
			schedulerstate.KeyEQ(strings.TrimSpace(key)),
		).
		Select(schedulerstate.FieldValueJSON).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("query scheduler state: %w", err)
	}
	return entity.ValueJSON, true, nil
}

func (s *schedulerStore) SetSchedulerState(ctx context.Context, schedulerID, key, valueJSON string) error {
	schedulerID = strings.TrimSpace(schedulerID)
	key = strings.TrimSpace(key)
	if schedulerID == "" || key == "" {
		return fmt.Errorf("scheduler state key is required")
	}
	valueJSON = strings.TrimSpace(valueJSON)
	now := time.Now().UTC().Unix()
	if err := s.client.SchedulerState.Create().
		SetID(schedulerID).
		SetKey(key).
		SetValueJSON(valueJSON).
		SetUpdatedAt(now).
		OnConflict(entsql.ConflictColumns(schedulerstate.FieldID, schedulerstate.FieldKey)).
		Update(func(u *dao.SchedulerStateUpsert) {
			u.SetValueJSON(valueJSON)
			u.SetUpdatedAt(now)
		}).
		Exec(ctx); err != nil {
		return fmt.Errorf("upsert scheduler state: %w", err)
	}
	return nil
}

func (s *schedulerStore) DeleteSchedulerState(ctx context.Context, schedulerID, key string) error {
	if _, err := s.client.SchedulerState.Delete().
		Where(
			schedulerstate.IDEQ(strings.TrimSpace(schedulerID)),
			schedulerstate.KeyEQ(strings.TrimSpace(key)),
		).
		Exec(ctx); err != nil {
		return fmt.Errorf("delete scheduler state: %w", err)
	}
	return nil
}

func (s *schedulerStore) GetSchedulerBinding(ctx context.Context, schedulerID, triggerID string) (domain.SchedulerBinding, bool, error) {
	entity, err := s.client.SchedulerSandboxBinding.Query().
		Where(
			schedulersandboxbinding.IDEQ(strings.TrimSpace(schedulerID)),
			schedulersandboxbinding.TriggerIDEQ(strings.TrimSpace(triggerID)),
		).
		Only(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return domain.SchedulerBinding{}, false, nil
		}
		return domain.SchedulerBinding{}, false, err
	}
	return schedulerBindingFromEntity(entity), true, nil
}

// GetSchedulerBindingForSandbox reports whether a sandbox is currently bound
// for sticky scheduler reuse. A bound sandbox must not be reclaimed as an
// orphan even when its stopped-runtime policy is remove: the scheduler keeps
// the binding so the next run can resume it.
func (s *schedulerStore) GetSchedulerBindingForSandbox(ctx context.Context, sandboxID string) (domain.SchedulerBinding, bool, error) {
	entity, err := s.client.SchedulerSandboxBinding.Query().
		Where(schedulersandboxbinding.SandboxIDEQ(strings.TrimSpace(sandboxID))).
		First(ctx)
	if err != nil {
		if dao.IsNotFound(err) {
			return domain.SchedulerBinding{}, false, nil
		}
		return domain.SchedulerBinding{}, false, err
	}
	return schedulerBindingFromEntity(entity), true, nil
}

func (s *schedulerStore) UpsertSchedulerBinding(ctx context.Context, binding domain.SchedulerBinding) error {
	binding.SchedulerID = strings.TrimSpace(binding.SchedulerID)
	binding.TriggerID = strings.TrimSpace(binding.TriggerID)
	binding.SandboxID = strings.TrimSpace(binding.SandboxID)
	binding.SandboxConfigHash = strings.TrimSpace(binding.SandboxConfigHash)
	binding.SchedulerConfigHash = strings.TrimSpace(binding.SchedulerConfigHash)
	if binding.SchedulerID == "" || binding.SandboxID == "" {
		return fmt.Errorf("scheduler binding requires scheduler id and sandbox id")
	}
	now := time.Now().UTC()
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = now
	}
	binding.UpdatedAt = now
	if err := s.client.SchedulerSandboxBinding.Create().
		SetID(binding.SchedulerID).
		SetTriggerID(binding.TriggerID).
		SetSandboxID(binding.SandboxID).
		SetSandboxConfigHash(binding.SandboxConfigHash).
		SetSchedulerConfigHash(binding.SchedulerConfigHash).
		SetCreatedAt(binding.CreatedAt.Unix()).
		SetUpdatedAt(binding.UpdatedAt.Unix()).
		OnConflict(entsql.ConflictColumns(schedulersandboxbinding.FieldID, schedulersandboxbinding.FieldTriggerID)).
		Update(func(u *dao.SchedulerSandboxBindingUpsert) {
			u.SetSandboxID(binding.SandboxID)
			u.SetSandboxConfigHash(binding.SandboxConfigHash)
			u.SetSchedulerConfigHash(binding.SchedulerConfigHash)
			u.SetUpdatedAt(binding.UpdatedAt.Unix())
		}).
		Exec(ctx); err != nil {
		return fmt.Errorf("upsert scheduler binding: %w", err)
	}
	return nil
}

func (s *schedulerStore) CompareAndSwapSchedulerBinding(ctx context.Context, expected *domain.SchedulerBinding, replacement domain.SchedulerBinding) (bool, error) {
	replacement.SchedulerID = strings.TrimSpace(replacement.SchedulerID)
	replacement.TriggerID = strings.TrimSpace(replacement.TriggerID)
	replacement.SandboxID = strings.TrimSpace(replacement.SandboxID)
	replacement.SandboxConfigHash = strings.TrimSpace(replacement.SandboxConfigHash)
	replacement.SchedulerConfigHash = strings.TrimSpace(replacement.SchedulerConfigHash)
	if replacement.SchedulerID == "" || replacement.SandboxID == "" {
		return false, fmt.Errorf("scheduler binding requires scheduler id and sandbox id")
	}
	now := time.Now().UTC()
	if expected == nil {
		if err := s.client.SchedulerSandboxBinding.Create().
			SetID(replacement.SchedulerID).
			SetTriggerID(replacement.TriggerID).
			SetSandboxID(replacement.SandboxID).
			SetSandboxConfigHash(replacement.SandboxConfigHash).
			SetSchedulerConfigHash(replacement.SchedulerConfigHash).
			SetCreatedAt(now.Unix()).
			SetUpdatedAt(now.Unix()).
			OnConflict(entsql.ConflictColumns(schedulersandboxbinding.FieldID, schedulersandboxbinding.FieldTriggerID), entsql.DoNothing()).
			Exec(ctx); err != nil {
			// ent surfaces an INSERT ... ON CONFLICT DO NOTHING that hit an
			// existing row as sql.ErrNoRows (no returned row); that means
			// another claim won the race.
			if errors.Is(err, sql.ErrNoRows) {
				return false, nil
			}
			return false, fmt.Errorf("claim scheduler binding: %w", err)
		}
		return true, nil
	}
	if strings.TrimSpace(expected.SchedulerID) != replacement.SchedulerID || strings.TrimSpace(expected.TriggerID) != replacement.TriggerID {
		return false, fmt.Errorf("scheduler binding replacement key does not match expected binding")
	}
	affected, err := s.client.SchedulerSandboxBinding.Update().
		Where(
			schedulersandboxbinding.IDEQ(replacement.SchedulerID),
			schedulersandboxbinding.TriggerIDEQ(replacement.TriggerID),
			schedulersandboxbinding.SandboxIDEQ(strings.TrimSpace(expected.SandboxID)),
			schedulersandboxbinding.SandboxConfigHashEQ(strings.TrimSpace(expected.SandboxConfigHash)),
		).
		SetSandboxID(replacement.SandboxID).
		SetSandboxConfigHash(replacement.SandboxConfigHash).
		SetSchedulerConfigHash(replacement.SchedulerConfigHash).
		SetUpdatedAt(now.Unix()).
		Save(ctx)
	if err != nil {
		return false, fmt.Errorf("replace scheduler binding: %w", err)
	}
	return affected == 1, nil
}

// schedulerTriggerFromEntity maps one scheduler_trigger row to the domain value.
func schedulerTriggerFromEntity(entity *dao.SchedulerTrigger) domain.SchedulerTrigger {
	return domain.SchedulerTrigger{
		SchedulerID: entity.ID,
		ID:          entity.TriggerID,
		Kind:        entity.Kind,
		Topic:       entity.Topic,
		IntervalMs:  entity.IntervalMs,
		Enabled:     entity.Enabled != 0,
		AutoID:      entity.AutoID != 0,
		SpecJSON:    entity.SpecJSON,
		NextFireAt:  storedtime.ParseStoredTime(entity.NextFireAt),
		LastFiredAt: storedtime.ParseStoredTime(entity.LastFiredAt),
	}
}

// schedulerRunFromEntity maps one scheduler_run row to the domain value.
func schedulerRunFromEntity(entity *dao.SchedulerRun) domain.SchedulerRunSummary {
	return domain.SchedulerRunSummary{
		ID:               entity.ID,
		SchedulerID:      entity.SchedulerID,
		TriggerID:        entity.TriggerID,
		TriggerKind:      entity.TriggerKind,
		TriggerSource:    entity.TriggerSource,
		Status:           entity.Status,
		StartedAt:        storedtime.ParseStoredTime(entity.StartedAt),
		CompletedAt:      storedtime.ParseStoredTime(entity.CompletedAt),
		DurationMs:       entity.DurationMs,
		Error:            entity.Error,
		ResultJSON:       entity.ResultJSON,
		PayloadJSON:      entity.PayloadJSON,
		SourceScriptHash: entity.SourceScriptSha256,
		ArtifactsDir:     entity.ArtifactsDir,
	}
}

// schedulerEventFromEntity maps one scheduler_event row to the domain value.
func schedulerEventFromEntity(entity *dao.SchedulerEvent) domain.SchedulerEvent {
	return domain.SchedulerEvent{
		ID:                  entity.EventID,
		SchedulerID:         entity.ID,
		RunID:               entity.SchedulerRunID,
		TriggerID:           entity.TriggerID,
		Type:                entity.Type,
		Level:               entity.Level,
		Message:             entity.Message,
		PayloadJSON:         entity.PayloadJSON,
		LinkedSandboxID:     entity.LinkedSandboxID,
		LinkedCellID:        entity.LinkedCellID,
		LinkedAgentThreadID: entity.LinkedAgentThreadID,
		CreatedAt:           storedtime.ParseStoredTime(entity.CreatedAt),
	}
}

// schedulerBindingFromEntity maps one scheduler_sandbox_binding row to the domain value.
func schedulerBindingFromEntity(entity *dao.SchedulerSandboxBinding) domain.SchedulerBinding {
	return domain.SchedulerBinding{
		SchedulerID:         entity.ID,
		TriggerID:           entity.TriggerID,
		SandboxID:           entity.SandboxID,
		SandboxConfigHash:   entity.SandboxConfigHash,
		SchedulerConfigHash: entity.SchedulerConfigHash,
		CreatedAt:           storedtime.ParseStoredTime(entity.CreatedAt),
		UpdatedAt:           storedtime.ParseStoredTime(entity.UpdatedAt),
	}
}
