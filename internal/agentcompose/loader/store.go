package loader

import (
	domaincap "agent-compose/internal/agentcompose/capability"
	configdomain "agent-compose/internal/agentcompose/config"
	driverpkg "agent-compose/internal/driver"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

const storedUnixMillisecondThreshold = configdomain.StoredUnixMillisecondThreshold

const (
	LoaderTriggerKindInterval = TriggerKindInterval
	LoaderTriggerKindEvent    = TriggerKindEvent
	LoaderTriggerKindTimeout  = TriggerKindTimeout
	LoaderTriggerKindCron     = TriggerKindCron
)

type Loader = Definition
type LoaderSummary = Summary
type LoaderTrigger = Trigger
type LoaderRunSummary = RunSummary
type LoaderEvent = Event
type LoaderBinding = Binding

type LoaderStore struct {
	db *sql.DB
}

func NewStore(db *sql.DB) *LoaderStore {
	return &LoaderStore{db: db}
}

func (s *LoaderStore) EnsureSchema(ctx context.Context) error {
	return s.ensureLoaderSchema(ctx)
}

func (s *LoaderStore) tableColumnTypes(ctx context.Context, tableName string) (map[string]string, error) {
	return configdomain.TableColumnTypes(ctx, s.db, tableName)
}

func (s *LoaderStore) ensureLoaderSchema(ctx context.Context) error {
	statements := []string{
		`CREATE TABLE IF NOT EXISTS loader (
            id TEXT PRIMARY KEY,
            name TEXT NOT NULL,
            description TEXT NOT NULL DEFAULT '',
            runtime TEXT NOT NULL DEFAULT 'scheduler',
            script TEXT NOT NULL,
            workspace_id TEXT NOT NULL DEFAULT '',
            agent_id TEXT NOT NULL DEFAULT '',
            driver TEXT NOT NULL DEFAULT '',
            guest_image TEXT NOT NULL DEFAULT '',
            default_agent TEXT NOT NULL DEFAULT 'codex',
            session_policy TEXT NOT NULL DEFAULT 'sticky',
            concurrency_policy TEXT NOT NULL DEFAULT 'skip',
            capset_ids TEXT NOT NULL DEFAULT '[]',
            env_json TEXT NOT NULL DEFAULT '[]',
            enabled INTEGER NOT NULL DEFAULT 1,
            last_error TEXT NOT NULL DEFAULT '',
            created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
            updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
        );`,
		`CREATE TABLE IF NOT EXISTS loader_trigger (
            loader_id TEXT NOT NULL,
            trigger_id TEXT NOT NULL,
            kind TEXT NOT NULL,
            topic TEXT NOT NULL DEFAULT '',
            interval_ms INTEGER NOT NULL DEFAULT 0,
            enabled INTEGER NOT NULL DEFAULT 1,
            auto_id INTEGER NOT NULL DEFAULT 0,
            spec_json TEXT NOT NULL DEFAULT '{}',
            next_fire_at INTEGER NOT NULL DEFAULT 0,
            last_fired_at INTEGER NOT NULL DEFAULT 0,
            PRIMARY KEY(loader_id, trigger_id),
            FOREIGN KEY(loader_id) REFERENCES loader(id) ON DELETE CASCADE
        );`,
		`CREATE INDEX IF NOT EXISTS idx_loader_trigger_schedule ON loader_trigger(enabled, kind, next_fire_at);`,
		`CREATE TABLE IF NOT EXISTS loader_run (
            loader_id TEXT NOT NULL,
            run_id TEXT NOT NULL,
            trigger_id TEXT NOT NULL DEFAULT '',
            trigger_kind TEXT NOT NULL DEFAULT '',
            trigger_source TEXT NOT NULL DEFAULT '',
            status TEXT NOT NULL DEFAULT '',
            started_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
            completed_at INTEGER NOT NULL DEFAULT 0,
            duration_ms INTEGER NOT NULL DEFAULT 0,
            error TEXT NOT NULL DEFAULT '',
            result_json TEXT NOT NULL DEFAULT '',
            payload_json TEXT NOT NULL DEFAULT '',
            source_script_sha256 TEXT NOT NULL DEFAULT '',
            artifacts_dir TEXT NOT NULL DEFAULT '',
            PRIMARY KEY(loader_id, run_id),
            FOREIGN KEY(loader_id) REFERENCES loader(id) ON DELETE CASCADE
        );`,
		`CREATE INDEX IF NOT EXISTS idx_loader_run_started ON loader_run(loader_id, started_at DESC);`,
		`CREATE TABLE IF NOT EXISTS loader_event (
            loader_id TEXT NOT NULL,
            event_id TEXT NOT NULL,
            run_id TEXT NOT NULL DEFAULT '',
            trigger_id TEXT NOT NULL DEFAULT '',
            type TEXT NOT NULL,
            level TEXT NOT NULL DEFAULT 'info',
            message TEXT NOT NULL DEFAULT '',
            payload_json TEXT NOT NULL DEFAULT '',
            linked_session_id TEXT NOT NULL DEFAULT '',
            linked_cell_id TEXT NOT NULL DEFAULT '',
            linked_agent_session_id TEXT NOT NULL DEFAULT '',
            created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
            PRIMARY KEY(loader_id, event_id),
            FOREIGN KEY(loader_id) REFERENCES loader(id) ON DELETE CASCADE
        );`,
		`CREATE INDEX IF NOT EXISTS idx_loader_event_created ON loader_event(loader_id, created_at DESC);`,
		`CREATE TABLE IF NOT EXISTS loader_state (
            loader_id TEXT NOT NULL,
            key TEXT NOT NULL,
            value_json TEXT NOT NULL,
            updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
            PRIMARY KEY(loader_id, key),
            FOREIGN KEY(loader_id) REFERENCES loader(id) ON DELETE CASCADE
        );`,
		`CREATE TABLE IF NOT EXISTS loader_binding (
            loader_id TEXT PRIMARY KEY,
            session_id TEXT NOT NULL,
            created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
            updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
        );`,
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("create loader schema: %w", err)
		}
	}
	if err := s.ensureLoaderAgentIDColumn(ctx); err != nil {
		return err
	}
	if err := s.migrateLoaderTimestampPrecision(ctx); err != nil {
		return err
	}
	if err := s.ensureLoaderCapabilityColumn(ctx); err != nil {
		return err
	}
	if err := s.ensureLoaderManagedColumns(ctx); err != nil {
		return err
	}
	return nil
}

func (s *LoaderStore) ensureLoaderManagedColumns(ctx context.Context) error {
	columns := []struct {
		name       string
		definition string
	}{
		{name: "managed_project_id", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "managed_project_revision", definition: "INTEGER NOT NULL DEFAULT 0"},
		{name: "managed_agent_name", definition: "TEXT NOT NULL DEFAULT ''"},
		{name: "managed_scheduler_id", definition: "TEXT NOT NULL DEFAULT ''"},
	}
	for _, column := range columns {
		if err := ensureColumn(ctx, s.db, "loader", column.name, column.definition); err != nil {
			return fmt.Errorf("ensure loader managed column %s: %w", column.name, err)
		}
	}
	return nil
}

func (s *LoaderStore) ensureLoaderCapabilityColumn(ctx context.Context) error {
	columnTypes, err := s.tableColumnTypes(ctx, "loader")
	if err != nil {
		return err
	}
	if _, ok := columnTypes["capset_ids"]; ok {
		return nil
	}
	if _, err := s.db.ExecContext(ctx, `ALTER TABLE loader ADD COLUMN capset_ids TEXT NOT NULL DEFAULT '[]'`); err != nil {
		return fmt.Errorf("migrate loader capability column: %w", err)
	}
	return nil
}

func (s *LoaderStore) ensureLoaderAgentIDColumn(ctx context.Context) error {
	if err := ensureColumn(ctx, s.db, "loader", "agent_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return fmt.Errorf("ensure loader agent_id column: %w", err)
	}
	return nil
}

func ensureColumn(ctx context.Context, db *sql.DB, table, column, definition string) error {
	rows, err := db.QueryContext(ctx, "PRAGMA table_info("+table+")")
	if err != nil {
		return err
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var cid int
		var name string
		var typ string
		var notNull int
		var defaultValue any
		var pk int
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.ExecContext(ctx, "ALTER TABLE "+table+" ADD COLUMN "+column+" "+definition)
	return err
}

func (s *LoaderStore) migrateLoaderTimestampPrecision(ctx context.Context) error {
	statements := []string{
		fmt.Sprintf(`UPDATE loader_trigger SET next_fire_at = next_fire_at * 1000 WHERE next_fire_at > 0 AND next_fire_at < %d`, storedUnixMillisecondThreshold),
		fmt.Sprintf(`UPDATE loader_trigger SET last_fired_at = last_fired_at * 1000 WHERE last_fired_at > 0 AND last_fired_at < %d`, storedUnixMillisecondThreshold),
		fmt.Sprintf(`UPDATE loader_run SET started_at = started_at * 1000 WHERE started_at > 0 AND started_at < %d`, storedUnixMillisecondThreshold),
		fmt.Sprintf(`UPDATE loader_run SET completed_at = completed_at * 1000 WHERE completed_at > 0 AND completed_at < %d`, storedUnixMillisecondThreshold),
		fmt.Sprintf(`UPDATE loader_event SET created_at = created_at * 1000 WHERE created_at > 0 AND created_at < %d`, storedUnixMillisecondThreshold),
	}
	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return fmt.Errorf("migrate loader timestamp precision: %w", err)
		}
	}
	return nil
}

func normalizeLoaderRuntime(runtime string) (string, error) {
	return NormalizeRuntime(runtime)
}

func normalizeLoaderTriggerKind(kind string) (string, error) {
	return NormalizeTriggerKind(kind)
}

func normalizeLoaderSessionPolicy(policy string) string {
	return NormalizeSessionPolicy(policy)
}

func normalizeLoaderConcurrencyPolicy(policy string) string {
	return NormalizeConcurrencyPolicy(policy)
}

func normalizeLoaderRunStatus(status string) string {
	return NormalizeRunStatus(status)
}

func normalizeAgentKind(agent string) string {
	return NormalizeAgentKind(agent)
}

func normalizeEnvItems(items []EnvVar) []EnvVar {
	return NormalizeEnvItems(items)
}

func normalizeCapsetIDs(ids []string) []string {
	return domaincap.NormalizeCapsetIDs(ids)
}

func loaderTriggerStableID(kind, topic string, intervalMs int64, callbackSource string, index int) string {
	return TriggerStableID(kind, topic, intervalMs, callbackSource, index)
}

func loaderSourceSHA(script string) string {
	return SourceSHA(script)
}

func loaderTriggerUsesSchedule(kind string) bool {
	return TriggerUsesSchedule(kind)
}

func loaderTriggerScheduledAt(now time.Time, delayMs int64) time.Time {
	return TriggerScheduledAt(now, delayMs)
}

func timeIsSet(value time.Time) bool {
	return TimeIsSet(value)
}

func nonZeroTimeUnixMilli(value time.Time) int64 {
	return NonZeroTimeUnixMilli(value)
}

func parseStoredLoaderTriggerTime(value any) time.Time {
	return configdomain.ParseStoredLoaderTriggerTime(value)
}

func parseStoredTime(value any) time.Time {
	return configdomain.ParseStoredTime(value)
}

func boolToInt(value bool) int {
	return configdomain.BoolToInt(value)
}

func defaultLoaderName(now time.Time) string {
	return "Loader " + now.UTC().Format("2006-01-02 15:04")
}

func normalizeLoader(item Loader, assignID bool) (Loader, error) {
	now := time.Now().UTC()
	item.Summary.ID = strings.TrimSpace(item.Summary.ID)
	if assignID && item.Summary.ID == "" {
		item.Summary.ID = uuid.NewString()
	}
	if item.Summary.ID == "" {
		return Loader{}, fmt.Errorf("loader id is required")
	}
	item.Summary.Name = strings.TrimSpace(item.Summary.Name)
	if item.Summary.Name == "" {
		item.Summary.Name = defaultLoaderName(now)
	}
	item.Summary.Description = strings.TrimSpace(item.Summary.Description)
	runtime, err := normalizeLoaderRuntime(item.Summary.Runtime)
	if err != nil {
		return Loader{}, err
	}
	item.Summary.Runtime = runtime
	item.Script = strings.ReplaceAll(item.Script, "\r\n", "\n")
	if strings.TrimSpace(item.Script) == "" {
		return Loader{}, fmt.Errorf("loader script is required")
	}
	item.Summary.WorkspaceID = strings.TrimSpace(item.Summary.WorkspaceID)
	item.Summary.AgentID = strings.TrimSpace(item.Summary.AgentID)
	item.Summary.Driver = strings.TrimSpace(item.Summary.Driver)
	if item.Summary.Driver != "" {
		driver, err := driverpkg.ResolveSessionRuntimeDriver(item.Summary.Driver, item.Summary.Driver)
		if err != nil {
			return Loader{}, err
		}
		item.Summary.Driver = driver
	}
	item.Summary.GuestImage = strings.TrimSpace(item.Summary.GuestImage)
	item.Summary.DefaultAgent = normalizeAgentKind(item.Summary.DefaultAgent)
	if item.Summary.DefaultAgent == "" {
		item.Summary.DefaultAgent = "codex"
	}
	item.Summary.SessionPolicy = normalizeLoaderSessionPolicy(item.Summary.SessionPolicy)
	item.Summary.ConcurrencyPolicy = normalizeLoaderConcurrencyPolicy(item.Summary.ConcurrencyPolicy)
	item.Summary.CapsetIDs = normalizeCapsetIDs(item.Summary.CapsetIDs)
	item.Summary.ManagedProjectID = strings.TrimSpace(item.Summary.ManagedProjectID)
	item.Summary.ManagedAgentName = strings.TrimSpace(item.Summary.ManagedAgentName)
	item.Summary.ManagedSchedulerID = strings.TrimSpace(item.Summary.ManagedSchedulerID)
	if item.Summary.ManagedProjectID == "" {
		item.Summary.ManagedRevision = 0
		item.Summary.ManagedAgentName = ""
		item.Summary.ManagedSchedulerID = ""
	} else {
		if item.Summary.ManagedAgentName == "" || item.Summary.ManagedSchedulerID == "" {
			return Loader{}, fmt.Errorf("managed loader agent name and scheduler id are required")
		}
		if item.Summary.ManagedRevision < 0 {
			return Loader{}, fmt.Errorf("managed loader project revision cannot be negative")
		}
	}
	item.EnvItems = normalizeEnvItems(item.EnvItems)
	item.Triggers = append([]LoaderTrigger(nil), item.Triggers...)
	return item, nil
}

func NormalizeLoader(item Definition, assignID bool) (Definition, error) {
	return normalizeLoader(item, assignID)
}

func normalizeLoaderTrigger(loaderID string, trigger LoaderTrigger) (LoaderTrigger, error) {
	trigger.LoaderID = strings.TrimSpace(loaderID)
	trigger.ID = strings.TrimSpace(trigger.ID)
	if trigger.LoaderID == "" {
		return LoaderTrigger{}, fmt.Errorf("loader id is required")
	}
	if trigger.ID == "" {
		return LoaderTrigger{}, fmt.Errorf("loader trigger id is required")
	}
	kind, err := normalizeLoaderTriggerKind(trigger.Kind)
	if err != nil {
		return LoaderTrigger{}, err
	}
	trigger.Kind = kind
	trigger.Topic = strings.TrimSpace(trigger.Topic)
	switch trigger.Kind {
	case LoaderTriggerKindInterval:
		if trigger.IntervalMs <= 0 {
			return LoaderTrigger{}, fmt.Errorf("loader interval trigger %s requires a positive interval", trigger.ID)
		}
		trigger.Topic = ""
	case LoaderTriggerKindEvent:
		if trigger.Topic == "" {
			return LoaderTrigger{}, fmt.Errorf("loader event trigger %s requires a topic", trigger.ID)
		}
		trigger.IntervalMs = 0
	case LoaderTriggerKindTimeout:
		if trigger.IntervalMs <= 0 {
			return LoaderTrigger{}, fmt.Errorf("loader timeout trigger %s requires a positive delay", trigger.ID)
		}
		trigger.Topic = ""
	case LoaderTriggerKindCron:
		trigger.Topic = ""
		trigger.IntervalMs = 0
		normalizedSpecJSON, err := normalizeLoaderCronSpecJSON(trigger.SpecJSON)
		if err != nil {
			return LoaderTrigger{}, fmt.Errorf("loader cron trigger %s: %w", trigger.ID, err)
		}
		trigger.SpecJSON = normalizedSpecJSON
	}
	trigger.SpecJSON = strings.TrimSpace(trigger.SpecJSON)
	if trigger.SpecJSON == "" {
		trigger.SpecJSON = "{}"
	}
	if !timeIsSet(trigger.NextFireAt) {
		trigger.NextFireAt = time.Time{}
	} else {
		trigger.NextFireAt = trigger.NextFireAt.UTC()
	}
	if !timeIsSet(trigger.LastFiredAt) {
		trigger.LastFiredAt = time.Time{}
	} else {
		trigger.LastFiredAt = trigger.LastFiredAt.UTC()
	}
	return trigger, nil
}

func NormalizeLoaderTrigger(loaderID string, trigger Trigger) (Trigger, error) {
	return normalizeLoaderTrigger(loaderID, trigger)
}

func encodeLoaderEnvItems(items []SessionEnvVar) (string, error) {
	normalized := normalizeEnvItems(items)
	if normalized == nil {
		normalized = []SessionEnvVar{}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode loader env items: %w", err)
	}
	return string(data), nil
}

func decodeLoaderEnvItems(raw string) ([]SessionEnvVar, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var items []SessionEnvVar
	if err := json.Unmarshal([]byte(raw), &items); err != nil {
		return nil, fmt.Errorf("decode loader env items: %w", err)
	}
	return normalizeEnvItems(items), nil
}

// encodeCapsetIDs marshals the capset id set to the JSON stored in the
// capset_ids column ("[]" when empty).
func encodeCapsetIDs(ids []string) (string, error) {
	normalized := normalizeCapsetIDs(ids)
	if normalized == nil {
		normalized = []string{}
	}
	data, err := json.Marshal(normalized)
	if err != nil {
		return "", fmt.Errorf("encode capset ids: %w", err)
	}
	return string(data), nil
}

func EncodeCapsetIDs(ids []string) (string, error) {
	return encodeCapsetIDs(ids)
}

func decodeCapsetIDs(raw string) []string {
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return nil
	}
	var ids []string
	if err := json.Unmarshal([]byte(raw), &ids); err != nil {
		return nil
	}
	return normalizeCapsetIDs(ids)
}

func DecodeCapsetIDs(raw string) []string {
	return decodeCapsetIDs(raw)
}

func (s *LoaderStore) CreateLoader(ctx context.Context, item Loader) (Loader, error) {
	normalized, err := normalizeLoader(item, true)
	if err != nil {
		return Loader{}, err
	}
	envJSON, err := encodeLoaderEnvItems(normalized.EnvItems)
	if err != nil {
		return Loader{}, err
	}
	capsetIDsJSON, err := encodeCapsetIDs(normalized.Summary.CapsetIDs)
	if err != nil {
		return Loader{}, err
	}
	now := time.Now().UTC()
	normalized.Summary.CreatedAt = now
	normalized.Summary.UpdatedAt = now
	_, err = s.db.ExecContext(ctx, `INSERT INTO loader(
        id, name, description, runtime, script, workspace_id, agent_id, driver, guest_image, default_agent, session_policy, concurrency_policy, capset_ids, env_json,
        managed_project_id, managed_project_revision, managed_agent_name, managed_scheduler_id, enabled, last_error, created_at, updated_at
    ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		normalized.Summary.ID,
		normalized.Summary.Name,
		normalized.Summary.Description,
		normalized.Summary.Runtime,
		normalized.Script,
		normalized.Summary.WorkspaceID,
		normalized.Summary.AgentID,
		normalized.Summary.Driver,
		normalized.Summary.GuestImage,
		normalized.Summary.DefaultAgent,
		normalized.Summary.SessionPolicy,
		normalized.Summary.ConcurrencyPolicy,
		capsetIDsJSON,
		envJSON,
		normalized.Summary.ManagedProjectID,
		normalized.Summary.ManagedRevision,
		normalized.Summary.ManagedAgentName,
		normalized.Summary.ManagedSchedulerID,
		boolToInt(normalized.Summary.Enabled),
		normalized.Summary.LastError,
		normalized.Summary.CreatedAt.Unix(),
		normalized.Summary.UpdatedAt.Unix(),
	)
	if err != nil {
		return Loader{}, fmt.Errorf("insert loader %s: %w", normalized.Summary.ID, err)
	}
	return normalized, nil
}

func (s *LoaderStore) UpdateLoader(ctx context.Context, item Loader) (Loader, error) {
	normalized, err := normalizeLoader(item, false)
	if err != nil {
		return Loader{}, err
	}
	existing, err := s.GetLoader(ctx, normalized.Summary.ID)
	if err != nil {
		return Loader{}, err
	}
	if normalized.Summary.ManagedProjectID == "" && normalized.Summary.ManagedAgentName == "" && normalized.Summary.ManagedSchedulerID == "" && normalized.Summary.ManagedRevision == 0 {
		normalized.Summary.ManagedProjectID = existing.Summary.ManagedProjectID
		normalized.Summary.ManagedRevision = existing.Summary.ManagedRevision
		normalized.Summary.ManagedAgentName = existing.Summary.ManagedAgentName
		normalized.Summary.ManagedSchedulerID = existing.Summary.ManagedSchedulerID
	}
	envJSON, err := encodeLoaderEnvItems(normalized.EnvItems)
	if err != nil {
		return Loader{}, err
	}
	capsetIDsJSON, err := encodeCapsetIDs(normalized.Summary.CapsetIDs)
	if err != nil {
		return Loader{}, err
	}
	normalized.Summary.CreatedAt = existing.Summary.CreatedAt
	normalized.Summary.UpdatedAt = time.Now().UTC()
	result, err := s.db.ExecContext(ctx, `UPDATE loader SET
        name = ?, description = ?, runtime = ?, script = ?, workspace_id = ?, agent_id = ?, driver = ?, guest_image = ?, default_agent = ?, session_policy = ?,
        concurrency_policy = ?, capset_ids = ?, env_json = ?, managed_project_id = ?, managed_project_revision = ?, managed_agent_name = ?, managed_scheduler_id = ?,
        enabled = ?, last_error = ?, updated_at = ?
        WHERE id = ?`,
		normalized.Summary.Name,
		normalized.Summary.Description,
		normalized.Summary.Runtime,
		normalized.Script,
		normalized.Summary.WorkspaceID,
		normalized.Summary.AgentID,
		normalized.Summary.Driver,
		normalized.Summary.GuestImage,
		normalized.Summary.DefaultAgent,
		normalized.Summary.SessionPolicy,
		normalized.Summary.ConcurrencyPolicy,
		capsetIDsJSON,
		envJSON,
		normalized.Summary.ManagedProjectID,
		normalized.Summary.ManagedRevision,
		normalized.Summary.ManagedAgentName,
		normalized.Summary.ManagedSchedulerID,
		boolToInt(normalized.Summary.Enabled),
		normalized.Summary.LastError,
		normalized.Summary.UpdatedAt.Unix(),
		normalized.Summary.ID,
	)
	if err != nil {
		return Loader{}, fmt.Errorf("update loader %s: %w", normalized.Summary.ID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return Loader{}, fmt.Errorf("loader %s not found", normalized.Summary.ID)
	}
	normalized.Summary.TriggerCount = existing.Summary.TriggerCount
	normalized.Summary.RunCount = existing.Summary.RunCount
	normalized.Summary.EventCount = existing.Summary.EventCount
	normalized.Summary.LatestRunAt = existing.Summary.LatestRunAt
	return normalized, nil
}

func (s *LoaderStore) UpsertManagedLoader(ctx context.Context, item Loader) (Loader, error) {
	normalized, err := normalizeLoader(item, false)
	if err != nil {
		return Loader{}, err
	}
	if normalized.Summary.ManagedProjectID == "" || normalized.Summary.ManagedAgentName == "" || normalized.Summary.ManagedSchedulerID == "" {
		return Loader{}, fmt.Errorf("managed project id, agent name, and scheduler id are required")
	}
	if existing, found, err := s.getLoaderIfExists(ctx, normalized.Summary.ID); err != nil {
		return Loader{}, err
	} else if found {
		normalized.Summary.CreatedAt = existing.Summary.CreatedAt
		return s.UpdateLoader(ctx, normalized)
	}
	return s.CreateLoader(ctx, normalized)
}

func (s *LoaderStore) getLoaderIfExists(ctx context.Context, loaderID string) (Loader, bool, error) {
	item, err := s.GetLoader(ctx, loaderID)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Loader{}, false, nil
		}
		return Loader{}, false, err
	}
	return item, true, nil
}

func (s *LoaderStore) DeleteLoader(ctx context.Context, loaderID string) error {
	loaderID = strings.TrimSpace(loaderID)
	if loaderID == "" {
		return fmt.Errorf("loader id is required")
	}
	result, err := s.db.ExecContext(ctx, `DELETE FROM loader WHERE id = ?`, loaderID)
	if err != nil {
		return fmt.Errorf("delete loader %s: %w", loaderID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("loader %s not found", loaderID)
	}
	_, _ = s.db.ExecContext(ctx, `DELETE FROM loader_binding WHERE loader_id = ?`, loaderID)
	return nil
}

func (s *LoaderStore) DisableLoadersByDefaultAgent(ctx context.Context, agentID string) (int, error) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return 0, fmt.Errorf("agent id is required")
	}
	now := time.Now().UTC().Unix()
	result, err := s.db.ExecContext(ctx, `UPDATE loader SET enabled = 0, updated_at = ? WHERE (agent_id = ? OR default_agent = ?) AND enabled = 1`, now, agentID, agentID)
	if err != nil {
		return 0, fmt.Errorf("disable loaders for agent %s: %w", agentID, err)
	}
	rows, _ := result.RowsAffected()
	return int(rows), nil
}

func (s *LoaderStore) ListLoaderSummaries(ctx context.Context) ([]LoaderSummary, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT
        l.id,
        l.name,
        l.description,
        l.runtime,
        l.workspace_id,
        l.agent_id,
        l.driver,
        l.guest_image,
        l.default_agent,
        l.session_policy,
        l.concurrency_policy,
        l.capset_ids,
        l.managed_project_id,
        l.managed_project_revision,
        l.managed_agent_name,
        l.managed_scheduler_id,
        l.enabled,
        l.last_error,
        l.created_at,
        l.updated_at,
        (SELECT COUNT(*) FROM loader_trigger t WHERE t.loader_id = l.id),
        (SELECT COUNT(*) FROM loader_run r WHERE r.loader_id = l.id),
        (SELECT COUNT(*) FROM loader_event e WHERE e.loader_id = l.id),
        (SELECT MAX(r.started_at) FROM loader_run r WHERE r.loader_id = l.id)
        FROM loader l
        ORDER BY l.updated_at DESC, l.created_at DESC, l.id DESC`)
	if err != nil {
		return nil, fmt.Errorf("query loaders: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]LoaderSummary, 0)
	for rows.Next() {
		item, err := scanLoaderSummary(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate loaders: %w", err)
	}
	return items, nil
}

func (s *LoaderStore) GetLoader(ctx context.Context, loaderID string) (Loader, error) {
	loaderID = strings.TrimSpace(loaderID)
	if loaderID == "" {
		return Loader{}, fmt.Errorf("loader id is required")
	}
	row := s.db.QueryRowContext(ctx, `SELECT
        id, name, description, runtime, script, workspace_id, agent_id, driver, guest_image, default_agent, session_policy, concurrency_policy, capset_ids, env_json,
        managed_project_id, managed_project_revision, managed_agent_name, managed_scheduler_id, enabled, last_error, created_at, updated_at
        FROM loader WHERE id = ?`, loaderID)
	item, err := scanLoader(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Loader{}, fmt.Errorf("loader %s not found: %w", loaderID, err)
		}
		return Loader{}, err
	}
	if err := s.hydrateLoaderSummaryCounts(ctx, &item.Summary); err != nil {
		return Loader{}, err
	}
	triggers, err := s.listLoaderTriggers(ctx, loaderID)
	if err != nil {
		return Loader{}, err
	}
	item.Triggers = triggers
	return item, nil
}

func (s *LoaderStore) ListLoaders(ctx context.Context) ([]Loader, error) {
	summaries, err := s.ListLoaderSummaries(ctx)
	if err != nil {
		return nil, err
	}
	items := make([]Loader, 0, len(summaries))
	for _, summary := range summaries {
		item, err := s.GetLoader(ctx, summary.ID)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *LoaderStore) ListManagedLoaders(ctx context.Context, projectID string) ([]Loader, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, fmt.Errorf("project id is required")
	}
	rows, err := s.db.QueryContext(ctx, `SELECT id FROM loader WHERE managed_project_id = ? ORDER BY managed_agent_name ASC, managed_scheduler_id ASC, id ASC`, projectID)
	if err != nil {
		return nil, fmt.Errorf("query managed loaders %s: %w", projectID, err)
	}
	defer func() { _ = rows.Close() }()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scan managed loader id: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate managed loaders %s: %w", projectID, err)
	}
	items := make([]Loader, 0, len(ids))
	for _, id := range ids {
		item, err := s.GetLoader(ctx, id)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func (s *LoaderStore) hydrateLoaderSummaryCounts(ctx context.Context, summary *LoaderSummary) error {
	if summary == nil || strings.TrimSpace(summary.ID) == "" {
		return nil
	}
	row := s.db.QueryRowContext(ctx, `SELECT
        (SELECT COUNT(*) FROM loader_trigger WHERE loader_id = ?),
        (SELECT COUNT(*) FROM loader_run WHERE loader_id = ?),
        (SELECT COUNT(*) FROM loader_event WHERE loader_id = ?),
        (SELECT MAX(started_at) FROM loader_run WHERE loader_id = ?)`, summary.ID, summary.ID, summary.ID, summary.ID)
	var triggerCount int
	var runCount int
	var eventCount int
	var latestRunAtRaw any
	if err := row.Scan(&triggerCount, &runCount, &eventCount, &latestRunAtRaw); err != nil {
		return fmt.Errorf("load loader summary counts: %w", err)
	}
	summary.TriggerCount = triggerCount
	summary.RunCount = runCount
	summary.EventCount = eventCount
	summary.LatestRunAt = parseStoredTime(latestRunAtRaw)
	return nil
}

func (s *LoaderStore) ReplaceLoaderTriggers(ctx context.Context, loaderID string, triggers []LoaderTrigger) ([]LoaderTrigger, error) {
	loaderID = strings.TrimSpace(loaderID)
	if loaderID == "" {
		return nil, fmt.Errorf("loader id is required")
	}
	existing, err := s.listLoaderTriggers(ctx, loaderID)
	if err != nil {
		return nil, err
	}
	existingByID := make(map[string]LoaderTrigger, len(existing))
	for _, item := range existing {
		existingByID[item.ID] = item
	}

	normalized := make([]LoaderTrigger, 0, len(triggers))
	seen := make(map[string]struct{}, len(triggers))
	now := time.Now().UTC()
	for _, trigger := range triggers {
		current, err := normalizeLoaderTrigger(loaderID, trigger)
		if err != nil {
			return nil, err
		}
		if _, ok := seen[current.ID]; ok {
			return nil, fmt.Errorf("duplicate loader trigger id %q", current.ID)
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
			case LoaderTriggerKindInterval:
				if current.NextFireAt.IsZero() {
					current.NextFireAt = loaderTriggerScheduledAt(now, current.IntervalMs)
				}
			case LoaderTriggerKindTimeout:
				if !sameSchedule {
					current.NextFireAt = loaderTriggerScheduledAt(now, current.IntervalMs)
				}
			case LoaderTriggerKindCron:
				if !sameSchedule || current.NextFireAt.IsZero() {
					nextFireAt, err := loaderTriggerNextFireAt(now, current, false)
					if err != nil {
						return nil, err
					}
					current.NextFireAt = nextFireAt
				}
			}
		}
		normalized = append(normalized, current)
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin loader trigger tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM loader_trigger WHERE loader_id = ?`, loaderID); err != nil {
		return nil, fmt.Errorf("reset loader triggers: %w", err)
	}
	for _, trigger := range normalized {
		if _, err := tx.ExecContext(ctx, `INSERT INTO loader_trigger(
            loader_id, trigger_id, kind, topic, interval_ms, enabled, auto_id, spec_json, next_fire_at, last_fired_at
        ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			trigger.LoaderID,
			trigger.ID,
			trigger.Kind,
			trigger.Topic,
			trigger.IntervalMs,
			boolToInt(trigger.Enabled),
			boolToInt(trigger.AutoID),
			trigger.SpecJSON,
			nonZeroTimeUnixMilli(trigger.NextFireAt),
			nonZeroTimeUnixMilli(trigger.LastFiredAt),
		); err != nil {
			return nil, fmt.Errorf("insert loader trigger %s/%s: %w", loaderID, trigger.ID, err)
		}
	}
	if _, err := tx.ExecContext(ctx, `UPDATE loader SET updated_at = ? WHERE id = ?`, time.Now().UTC().Unix(), loaderID); err != nil {
		return nil, fmt.Errorf("touch loader after trigger replace: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit loader trigger tx: %w", err)
	}
	sort.Slice(normalized, func(i, j int) bool {
		if normalized[i].Kind != normalized[j].Kind {
			return normalized[i].Kind < normalized[j].Kind
		}
		return normalized[i].ID < normalized[j].ID
	})
	return normalized, nil
}

func (s *LoaderStore) listLoaderTriggers(ctx context.Context, loaderID string) ([]LoaderTrigger, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT loader_id, trigger_id, kind, topic, interval_ms, enabled, auto_id, spec_json, next_fire_at, last_fired_at
        FROM loader_trigger WHERE loader_id = ? ORDER BY kind ASC, trigger_id ASC`, loaderID)
	if err != nil {
		return nil, fmt.Errorf("query loader triggers: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]LoaderTrigger, 0)
	for rows.Next() {
		item, err := scanLoaderTrigger(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate loader triggers: %w", err)
	}
	return items, nil
}

func (s *LoaderStore) SetLoaderEnabled(ctx context.Context, loaderID string, enabled bool) error {
	loaderID = strings.TrimSpace(loaderID)
	if loaderID == "" {
		return fmt.Errorf("loader id is required")
	}
	var (
		triggers []LoaderTrigger
		err      error
	)
	if enabled {
		triggers, err = s.listLoaderTriggers(ctx, loaderID)
		if err != nil {
			return err
		}
	}
	now := time.Now().UTC()
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin loader enable tx: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `UPDATE loader SET enabled = ?, updated_at = ? WHERE id = ?`, boolToInt(enabled), now.Unix(), loaderID)
	if err != nil {
		return fmt.Errorf("update loader enabled state: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("loader %s not found", loaderID)
	}
	if enabled {
		for _, trigger := range triggers {
			if !trigger.Enabled || !loaderTriggerUsesSchedule(trigger.Kind) {
				continue
			}
			nextFireAt, err := loaderTriggerNextFireAt(now, trigger, false)
			if err != nil {
				return fmt.Errorf("schedule loader trigger %s/%s: %w", loaderID, trigger.ID, err)
			}
			if _, err := tx.ExecContext(ctx, `UPDATE loader_trigger SET next_fire_at = ? WHERE loader_id = ? AND trigger_id = ?`, nonZeroTimeUnixMilli(nextFireAt), loaderID, trigger.ID); err != nil {
				return fmt.Errorf("schedule loader trigger %s/%s: %w", loaderID, trigger.ID, err)
			}
		}
	} else {
		if _, err := tx.ExecContext(ctx, `UPDATE loader_trigger SET next_fire_at = 0 WHERE loader_id = ? AND kind IN (?, ?, ?)`, loaderID, LoaderTriggerKindInterval, LoaderTriggerKindTimeout, LoaderTriggerKindCron); err != nil {
			return fmt.Errorf("pause loader scheduled triggers: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit loader enable tx: %w", err)
	}
	return nil
}

func (s *LoaderStore) SetLoaderTriggerEnabled(ctx context.Context, loaderID, triggerID string, enabled bool) error {
	loaderID = strings.TrimSpace(loaderID)
	triggerID = strings.TrimSpace(triggerID)
	if loaderID == "" || triggerID == "" {
		return fmt.Errorf("loader trigger id is required")
	}
	row := s.db.QueryRowContext(ctx, `SELECT loader_id, trigger_id, kind, topic, interval_ms, enabled, auto_id, spec_json, next_fire_at, last_fired_at
        FROM loader_trigger WHERE loader_id = ? AND trigger_id = ?`, loaderID, triggerID)
	trigger, err := scanLoaderTrigger(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("loader trigger %s/%s not found: %w", loaderID, triggerID, err)
		}
		return err
	}
	nextFireAt := int64(0)
	if enabled && loaderTriggerUsesSchedule(trigger.Kind) {
		scheduledAt, err := loaderTriggerNextFireAt(time.Now().UTC(), trigger, false)
		if err != nil {
			return fmt.Errorf("schedule loader trigger %s/%s: %w", loaderID, triggerID, err)
		}
		nextFireAt = nonZeroTimeUnixMilli(scheduledAt)
	}
	result, err := s.db.ExecContext(ctx, `UPDATE loader_trigger SET enabled = ?, next_fire_at = ? WHERE loader_id = ? AND trigger_id = ?`, boolToInt(enabled), nextFireAt, loaderID, triggerID)
	if err != nil {
		return fmt.Errorf("update loader trigger enabled state: %w", err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("loader trigger %s/%s not found", loaderID, triggerID)
	}
	_, _ = s.db.ExecContext(ctx, `UPDATE loader SET updated_at = ? WHERE id = ?`, time.Now().UTC().Unix(), loaderID)
	return nil
}

func (s *LoaderStore) UpdateLoaderLastError(ctx context.Context, loaderID, lastError string) error {
	loaderID = strings.TrimSpace(loaderID)
	if loaderID == "" {
		return fmt.Errorf("loader id is required")
	}
	_, err := s.db.ExecContext(ctx, `UPDATE loader SET last_error = ?, updated_at = ? WHERE id = ?`, strings.TrimSpace(lastError), time.Now().UTC().Unix(), loaderID)
	if err != nil {
		return fmt.Errorf("update loader last error: %w", err)
	}
	return nil
}

func (s *LoaderStore) MarkLoaderTriggerFired(ctx context.Context, loaderID, triggerID string, lastFiredAt, nextFireAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `UPDATE loader_trigger SET last_fired_at = ?, next_fire_at = ? WHERE loader_id = ? AND trigger_id = ?`, nonZeroTimeUnixMilli(lastFiredAt), nonZeroTimeUnixMilli(nextFireAt), strings.TrimSpace(loaderID), strings.TrimSpace(triggerID))
	if err != nil {
		return fmt.Errorf("update loader trigger fire state: %w", err)
	}
	return nil
}

func (s *LoaderStore) CreateLoaderRun(ctx context.Context, run LoaderRunSummary) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO loader_run(
        loader_id, run_id, trigger_id, trigger_kind, trigger_source, status, started_at, completed_at, duration_ms, error, result_json, payload_json, source_script_sha256, artifacts_dir
    ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(run.LoaderID),
		strings.TrimSpace(run.ID),
		strings.TrimSpace(run.TriggerID),
		strings.TrimSpace(run.TriggerKind),
		strings.TrimSpace(run.TriggerSource),
		normalizeLoaderRunStatus(run.Status),
		run.StartedAt.UTC().UnixMilli(),
		nonZeroTimeUnixMilli(run.CompletedAt),
		run.DurationMs,
		run.Error,
		run.ResultJSON,
		run.PayloadJSON,
		run.SourceScriptHash,
		strings.TrimSpace(run.ArtifactsDir),
	)
	if err != nil {
		return fmt.Errorf("insert loader run %s/%s: %w", run.LoaderID, run.ID, err)
	}
	return nil
}

func (s *LoaderStore) UpdateLoaderRun(ctx context.Context, run LoaderRunSummary) error {
	result, err := s.db.ExecContext(ctx, `UPDATE loader_run SET
        trigger_id = ?, trigger_kind = ?, trigger_source = ?, status = ?, started_at = ?, completed_at = ?, duration_ms = ?, error = ?, result_json = ?, payload_json = ?, source_script_sha256 = ?, artifacts_dir = ?
        WHERE loader_id = ? AND run_id = ?`,
		strings.TrimSpace(run.TriggerID),
		strings.TrimSpace(run.TriggerKind),
		strings.TrimSpace(run.TriggerSource),
		normalizeLoaderRunStatus(run.Status),
		run.StartedAt.UTC().UnixMilli(),
		nonZeroTimeUnixMilli(run.CompletedAt),
		run.DurationMs,
		run.Error,
		run.ResultJSON,
		run.PayloadJSON,
		run.SourceScriptHash,
		strings.TrimSpace(run.ArtifactsDir),
		strings.TrimSpace(run.LoaderID),
		strings.TrimSpace(run.ID),
	)
	if err != nil {
		return fmt.Errorf("update loader run %s/%s: %w", run.LoaderID, run.ID, err)
	}
	if rows, _ := result.RowsAffected(); rows == 0 {
		return fmt.Errorf("loader run %s/%s not found", run.LoaderID, run.ID)
	}
	return nil
}

func (s *LoaderStore) GetLoaderRun(ctx context.Context, loaderID, runID string) (LoaderRunSummary, error) {
	row := s.db.QueryRowContext(ctx, `SELECT loader_id, run_id, trigger_id, trigger_kind, trigger_source, status, started_at, completed_at, duration_ms, error, result_json, payload_json, source_script_sha256, artifacts_dir
        FROM loader_run WHERE loader_id = ? AND run_id = ?`, strings.TrimSpace(loaderID), strings.TrimSpace(runID))
	item, err := scanLoaderRun(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LoaderRunSummary{}, fmt.Errorf("loader run %s/%s not found: %w", loaderID, runID, err)
		}
		return LoaderRunSummary{}, err
	}
	return item, nil
}

func (s *LoaderStore) ListLoaderRuns(ctx context.Context, loaderID string, limit int) ([]LoaderRunSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT loader_id, run_id, trigger_id, trigger_kind, trigger_source, status, started_at, completed_at, duration_ms, error, result_json, payload_json, source_script_sha256, artifacts_dir
        FROM loader_run WHERE loader_id = ? ORDER BY started_at DESC, run_id DESC LIMIT ?`, strings.TrimSpace(loaderID), limit)
	if err != nil {
		return nil, fmt.Errorf("query loader runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]LoaderRunSummary, 0)
	for rows.Next() {
		item, err := scanLoaderRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate loader runs: %w", err)
	}
	return items, nil
}

func (s *LoaderStore) ListRecentLoaderRuns(ctx context.Context, limit int) ([]LoaderRunSummary, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `SELECT loader_id, run_id, trigger_id, trigger_kind, trigger_source, status, started_at, completed_at, duration_ms, error, result_json, payload_json, source_script_sha256, artifacts_dir
        FROM loader_run ORDER BY started_at DESC, loader_id DESC, run_id DESC LIMIT ?`, limit)
	if err != nil {
		return nil, fmt.Errorf("query recent loader runs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]LoaderRunSummary, 0)
	for rows.Next() {
		item, err := scanLoaderRun(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate recent loader runs: %w", err)
	}
	return items, nil
}

func (s *LoaderStore) AddLoaderEvent(ctx context.Context, event LoaderEvent) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO loader_event(
        loader_id, event_id, run_id, trigger_id, type, level, message, payload_json, linked_session_id, linked_cell_id, linked_agent_session_id, created_at
    ) VALUES(?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		strings.TrimSpace(event.LoaderID),
		strings.TrimSpace(event.ID),
		strings.TrimSpace(event.RunID),
		strings.TrimSpace(event.TriggerID),
		strings.TrimSpace(event.Type),
		strings.TrimSpace(event.Level),
		strings.TrimSpace(event.Message),
		strings.TrimSpace(event.PayloadJSON),
		strings.TrimSpace(event.LinkedSessionID),
		strings.TrimSpace(event.LinkedCellID),
		strings.TrimSpace(event.LinkedAgentSessionID),
		event.CreatedAt.UTC().UnixMilli(),
	)
	if err != nil {
		return fmt.Errorf("insert loader event %s/%s: %w", event.LoaderID, event.ID, err)
	}
	return nil
}

func (s *LoaderStore) ListLoaderEvents(ctx context.Context, loaderID string, limit int) ([]LoaderEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `SELECT loader_id, event_id, run_id, trigger_id, type, level, message, payload_json, linked_session_id, linked_cell_id, linked_agent_session_id, created_at
        FROM loader_event WHERE loader_id = ? ORDER BY created_at DESC, event_id DESC LIMIT ?`, strings.TrimSpace(loaderID), limit)
	if err != nil {
		return nil, fmt.Errorf("query loader events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	items := make([]LoaderEvent, 0)
	for rows.Next() {
		item, err := scanLoaderEvent(rows.Scan)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate loader events: %w", err)
	}
	return items, nil
}

func (s *LoaderStore) GetLoaderState(ctx context.Context, loaderID, key string) (string, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT value_json FROM loader_state WHERE loader_id = ? AND key = ?`, strings.TrimSpace(loaderID), strings.TrimSpace(key))
	var value string
	if err := row.Scan(&value); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("query loader state: %w", err)
	}
	return value, true, nil
}

func (s *LoaderStore) SetLoaderState(ctx context.Context, loaderID, key, valueJSON string) error {
	loaderID = strings.TrimSpace(loaderID)
	key = strings.TrimSpace(key)
	if loaderID == "" || key == "" {
		return fmt.Errorf("loader state key is required")
	}
	_, err := s.db.ExecContext(ctx, `INSERT INTO loader_state(loader_id, key, value_json, updated_at) VALUES(?, ?, ?, ?)
        ON CONFLICT(loader_id, key) DO UPDATE SET value_json = excluded.value_json, updated_at = excluded.updated_at`, loaderID, key, strings.TrimSpace(valueJSON), time.Now().UTC().Unix())
	if err != nil {
		return fmt.Errorf("upsert loader state: %w", err)
	}
	return nil
}

func (s *LoaderStore) DeleteLoaderState(ctx context.Context, loaderID, key string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM loader_state WHERE loader_id = ? AND key = ?`, strings.TrimSpace(loaderID), strings.TrimSpace(key))
	if err != nil {
		return fmt.Errorf("delete loader state: %w", err)
	}
	return nil
}

func (s *LoaderStore) GetLoaderBinding(ctx context.Context, loaderID string) (LoaderBinding, bool, error) {
	row := s.db.QueryRowContext(ctx, `SELECT loader_id, session_id, created_at, updated_at FROM loader_binding WHERE loader_id = ?`, strings.TrimSpace(loaderID))
	item, err := scanLoaderBinding(row.Scan)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return LoaderBinding{}, false, nil
		}
		return LoaderBinding{}, false, err
	}
	return item, true, nil
}

func (s *LoaderStore) UpsertLoaderBinding(ctx context.Context, binding LoaderBinding) error {
	binding.LoaderID = strings.TrimSpace(binding.LoaderID)
	binding.SessionID = strings.TrimSpace(binding.SessionID)
	if binding.LoaderID == "" || binding.SessionID == "" {
		return fmt.Errorf("loader binding requires loader id and session id")
	}
	now := time.Now().UTC()
	if binding.CreatedAt.IsZero() {
		binding.CreatedAt = now
	}
	binding.UpdatedAt = now
	_, err := s.db.ExecContext(ctx, `INSERT INTO loader_binding(loader_id, session_id, created_at, updated_at) VALUES(?, ?, ?, ?)
        ON CONFLICT(loader_id) DO UPDATE SET session_id = excluded.session_id, updated_at = excluded.updated_at`, binding.LoaderID, binding.SessionID, binding.CreatedAt.Unix(), binding.UpdatedAt.Unix())
	if err != nil {
		return fmt.Errorf("upsert loader binding: %w", err)
	}
	return nil
}

func scanLoaderSummary(scan func(dest ...any) error) (LoaderSummary, error) {
	var item LoaderSummary
	var enabled int
	var capsetIDsRaw string
	var createdAtRaw any
	var updatedAtRaw any
	var latestRunAtRaw any
	if err := scan(
		&item.ID,
		&item.Name,
		&item.Description,
		&item.Runtime,
		&item.WorkspaceID,
		&item.AgentID,
		&item.Driver,
		&item.GuestImage,
		&item.DefaultAgent,
		&item.SessionPolicy,
		&item.ConcurrencyPolicy,
		&capsetIDsRaw,
		&item.ManagedProjectID,
		&item.ManagedRevision,
		&item.ManagedAgentName,
		&item.ManagedSchedulerID,
		&enabled,
		&item.LastError,
		&createdAtRaw,
		&updatedAtRaw,
		&item.TriggerCount,
		&item.RunCount,
		&item.EventCount,
		&latestRunAtRaw,
	); err != nil {
		return LoaderSummary{}, fmt.Errorf("scan loader summary: %w", err)
	}
	item.CapsetIDs = decodeCapsetIDs(capsetIDsRaw)
	item.ManagedProjectID = strings.TrimSpace(item.ManagedProjectID)
	item.ManagedAgentName = strings.TrimSpace(item.ManagedAgentName)
	item.ManagedSchedulerID = strings.TrimSpace(item.ManagedSchedulerID)
	item.Enabled = enabled != 0
	item.CreatedAt = parseStoredTime(createdAtRaw)
	item.UpdatedAt = parseStoredTime(updatedAtRaw)
	item.LatestRunAt = parseStoredTime(latestRunAtRaw)
	return item, nil
}

func scanLoader(scan func(dest ...any) error) (Loader, error) {
	var item Loader
	var enabled int
	var envJSON string
	var capsetIDsRaw string
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(
		&item.Summary.ID,
		&item.Summary.Name,
		&item.Summary.Description,
		&item.Summary.Runtime,
		&item.Script,
		&item.Summary.WorkspaceID,
		&item.Summary.AgentID,
		&item.Summary.Driver,
		&item.Summary.GuestImage,
		&item.Summary.DefaultAgent,
		&item.Summary.SessionPolicy,
		&item.Summary.ConcurrencyPolicy,
		&capsetIDsRaw,
		&envJSON,
		&item.Summary.ManagedProjectID,
		&item.Summary.ManagedRevision,
		&item.Summary.ManagedAgentName,
		&item.Summary.ManagedSchedulerID,
		&enabled,
		&item.Summary.LastError,
		&createdAtRaw,
		&updatedAtRaw,
	); err != nil {
		return Loader{}, fmt.Errorf("scan loader: %w", err)
	}
	item.Summary.CapsetIDs = decodeCapsetIDs(capsetIDsRaw)
	item.Summary.ManagedProjectID = strings.TrimSpace(item.Summary.ManagedProjectID)
	item.Summary.ManagedAgentName = strings.TrimSpace(item.Summary.ManagedAgentName)
	item.Summary.ManagedSchedulerID = strings.TrimSpace(item.Summary.ManagedSchedulerID)
	item.Summary.Enabled = enabled != 0
	item.Summary.CreatedAt = parseStoredTime(createdAtRaw)
	item.Summary.UpdatedAt = parseStoredTime(updatedAtRaw)
	envItems, err := decodeLoaderEnvItems(envJSON)
	if err != nil {
		return Loader{}, err
	}
	item.EnvItems = envItems
	return item, nil
}

func scanLoaderTrigger(scan func(dest ...any) error) (LoaderTrigger, error) {
	var item LoaderTrigger
	var enabled int
	var autoID int
	var nextFireAtRaw any
	var lastFiredAtRaw any
	if err := scan(&item.LoaderID, &item.ID, &item.Kind, &item.Topic, &item.IntervalMs, &enabled, &autoID, &item.SpecJSON, &nextFireAtRaw, &lastFiredAtRaw); err != nil {
		return LoaderTrigger{}, fmt.Errorf("scan loader trigger: %w", err)
	}
	item.Enabled = enabled != 0
	item.AutoID = autoID != 0
	item.NextFireAt = parseStoredLoaderTriggerTime(nextFireAtRaw)
	item.LastFiredAt = parseStoredLoaderTriggerTime(lastFiredAtRaw)
	return item, nil
}

func scanLoaderRun(scan func(dest ...any) error) (LoaderRunSummary, error) {
	var item LoaderRunSummary
	var startedAtRaw any
	var completedAtRaw any
	if err := scan(&item.LoaderID, &item.ID, &item.TriggerID, &item.TriggerKind, &item.TriggerSource, &item.Status, &startedAtRaw, &completedAtRaw, &item.DurationMs, &item.Error, &item.ResultJSON, &item.PayloadJSON, &item.SourceScriptHash, &item.ArtifactsDir); err != nil {
		return LoaderRunSummary{}, fmt.Errorf("scan loader run: %w", err)
	}
	item.StartedAt = parseStoredTime(startedAtRaw)
	item.CompletedAt = parseStoredTime(completedAtRaw)
	return item, nil
}

func scanLoaderEvent(scan func(dest ...any) error) (LoaderEvent, error) {
	var item LoaderEvent
	var createdAtRaw any
	if err := scan(&item.LoaderID, &item.ID, &item.RunID, &item.TriggerID, &item.Type, &item.Level, &item.Message, &item.PayloadJSON, &item.LinkedSessionID, &item.LinkedCellID, &item.LinkedAgentSessionID, &createdAtRaw); err != nil {
		return LoaderEvent{}, fmt.Errorf("scan loader event: %w", err)
	}
	item.CreatedAt = parseStoredTime(createdAtRaw)
	return item, nil
}

func scanLoaderBinding(scan func(dest ...any) error) (LoaderBinding, error) {
	var item LoaderBinding
	var createdAtRaw any
	var updatedAtRaw any
	if err := scan(&item.LoaderID, &item.SessionID, &createdAtRaw, &updatedAtRaw); err != nil {
		return LoaderBinding{}, fmt.Errorf("scan loader binding: %w", err)
	}
	item.CreatedAt = parseStoredTime(createdAtRaw)
	item.UpdatedAt = parseStoredTime(updatedAtRaw)
	return item, nil
}
