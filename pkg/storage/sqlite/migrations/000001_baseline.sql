-- Baseline schema for agent-compose's SQLite configuration database.
-- This file is immutable after release; add a new numbered migration instead.

CREATE TABLE IF NOT EXISTS global_env (
    name TEXT PRIMARY KEY,
    value TEXT NOT NULL,
    secret INTEGER NOT NULL DEFAULT 0,
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
);

CREATE TABLE IF NOT EXISTS workspace_config (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    type TEXT NOT NULL,
    config_json TEXT NOT NULL DEFAULT '{}',
    comment TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
);

CREATE TABLE IF NOT EXISTS agent_definition (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    deleted_at INTEGER NOT NULL DEFAULT 0,
    provider TEXT NOT NULL DEFAULT 'codex',
    model TEXT NOT NULL DEFAULT '',
    system_prompt TEXT NOT NULL DEFAULT '',
    driver TEXT NOT NULL DEFAULT '',
    guest_image TEXT NOT NULL DEFAULT '',
    workspace_id TEXT NOT NULL DEFAULT '',
    env_json TEXT NOT NULL DEFAULT '[]',
    volumes_json TEXT NOT NULL DEFAULT '[]',
    config_json TEXT NOT NULL DEFAULT '{}',
    capset_ids TEXT NOT NULL DEFAULT '[]',
    skills TEXT NOT NULL DEFAULT '[]',
    managed_project_id TEXT NOT NULL DEFAULT '',
    managed_project_revision INTEGER NOT NULL DEFAULT 0,
    managed_agent_name TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_agent_definition_deleted_enabled
    ON agent_definition(deleted_at, enabled);
CREATE INDEX IF NOT EXISTS idx_agent_definition_workspace
    ON agent_definition(workspace_id);
CREATE INDEX IF NOT EXISTS idx_agent_definition_managed_project
    ON agent_definition(managed_project_id, managed_agent_name);

CREATE TABLE IF NOT EXISTS llm_provider (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    provider_type TEXT NOT NULL DEFAULT 'openai_compatible',
    default_wire_api TEXT NOT NULL DEFAULT 'responses',
    base_url TEXT NOT NULL,
    api_key TEXT NOT NULL DEFAULT '',
    auth_header TEXT NOT NULL DEFAULT 'Authorization',
    auth_scheme TEXT NOT NULL DEFAULT 'Bearer',
    headers_json TEXT NOT NULL DEFAULT '{}',
    use_generic_responses_text_parts INTEGER NOT NULL DEFAULT 0,
    weight INTEGER NOT NULL DEFAULT 10,
    enabled INTEGER NOT NULL DEFAULT 1,
    scope TEXT NOT NULL DEFAULT 'system',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS llm_model (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    description TEXT NOT NULL DEFAULT '',
    default_model INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    scope TEXT NOT NULL DEFAULT 'system',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS llm_provider_model (
    provider_id TEXT NOT NULL,
    model_id TEXT NOT NULL,
    wire_api TEXT NOT NULL DEFAULT '',
    weight INTEGER NOT NULL DEFAULT 10,
    PRIMARY KEY(provider_id, model_id)
);

CREATE TABLE IF NOT EXISTS llm_facade_token (
    token_hash TEXT PRIMARY KEY,
    sandbox_id TEXT NOT NULL,
    token_fingerprint TEXT NOT NULL,
    model TEXT NOT NULL DEFAULT '',
    provider_id TEXT NOT NULL DEFAULT '',
    wire_api TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL DEFAULT '',
    issued_at INTEGER NOT NULL,
    expires_at INTEGER NOT NULL,
    revoked_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_llm_facade_token_sandbox
    ON llm_facade_token(sandbox_id, revoked_at, expires_at);

CREATE TABLE IF NOT EXISTS capability_gateway (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    addr TEXT NOT NULL DEFAULT '',
    token TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
);

CREATE TABLE IF NOT EXISTS volumes (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL UNIQUE,
    driver TEXT NOT NULL DEFAULT 'local',
    path TEXT NOT NULL DEFAULT '',
    labels_json TEXT NOT NULL DEFAULT '{}',
    options_json TEXT NOT NULL DEFAULT '{}',
    project_id TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
);
CREATE INDEX IF NOT EXISTS idx_volumes_driver ON volumes(driver);
CREATE INDEX IF NOT EXISTS idx_volumes_project ON volumes(project_id);

CREATE TABLE IF NOT EXISTS project_volumes (
    project_id TEXT NOT NULL,
    volume_key TEXT NOT NULL,
    volume_id TEXT NOT NULL,
    external INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    PRIMARY KEY(project_id, volume_key),
    FOREIGN KEY(volume_id) REFERENCES volumes(id) ON DELETE RESTRICT
);
CREATE INDEX IF NOT EXISTS idx_project_volumes_volume ON project_volumes(volume_id);

CREATE TABLE IF NOT EXISTS loader (
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
    sandbox_policy TEXT NOT NULL DEFAULT 'sticky',
    concurrency_policy TEXT NOT NULL DEFAULT 'skip',
    capset_ids TEXT NOT NULL DEFAULT '[]',
    env_json TEXT NOT NULL DEFAULT '[]',
    volumes_json TEXT NOT NULL DEFAULT '[]',
    managed_project_id TEXT NOT NULL DEFAULT '',
    managed_project_revision INTEGER NOT NULL DEFAULT 0,
    managed_agent_name TEXT NOT NULL DEFAULT '',
    managed_scheduler_id TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 1,
    last_error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER))
);
CREATE INDEX IF NOT EXISTS idx_loader_managed_project
    ON loader(managed_project_id, managed_agent_name, managed_scheduler_id);

CREATE TABLE IF NOT EXISTS loader_trigger (
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
);
CREATE INDEX IF NOT EXISTS idx_loader_trigger_schedule
    ON loader_trigger(enabled, kind, next_fire_at);

CREATE TABLE IF NOT EXISTS loader_run (
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
);
CREATE INDEX IF NOT EXISTS idx_loader_run_started
    ON loader_run(loader_id, started_at DESC);
CREATE INDEX IF NOT EXISTS idx_loader_run_trigger_started
    ON loader_run(loader_id, trigger_id, started_at DESC, run_id DESC);
CREATE INDEX IF NOT EXISTS idx_loader_run_status_started
    ON loader_run(loader_id, status, started_at DESC, run_id DESC);
CREATE INDEX IF NOT EXISTS idx_loader_run_prune
    ON loader_run(loader_id, status, completed_at, started_at, run_id);

CREATE TABLE IF NOT EXISTS loader_event (
    loader_id TEXT NOT NULL,
    event_id TEXT NOT NULL,
    run_id TEXT NOT NULL DEFAULT '',
    trigger_id TEXT NOT NULL DEFAULT '',
    type TEXT NOT NULL,
    level TEXT NOT NULL DEFAULT 'info',
    message TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL DEFAULT '',
    linked_sandbox_id TEXT NOT NULL DEFAULT '',
    linked_cell_id TEXT NOT NULL DEFAULT '',
    linked_agent_thread_id TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    PRIMARY KEY(loader_id, event_id),
    FOREIGN KEY(loader_id) REFERENCES loader(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_loader_event_created
    ON loader_event(loader_id, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_loader_event_run_created
    ON loader_event(loader_id, run_id, created_at DESC, event_id DESC);

CREATE TABLE IF NOT EXISTS loader_state (
    loader_id TEXT NOT NULL,
    key TEXT NOT NULL,
    value_json TEXT NOT NULL,
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    PRIMARY KEY(loader_id, key),
    FOREIGN KEY(loader_id) REFERENCES loader(id) ON DELETE CASCADE
);

CREATE TABLE IF NOT EXISTS loader_binding (
    loader_id TEXT NOT NULL,
    trigger_id TEXT NOT NULL DEFAULT '',
    sandbox_id TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    PRIMARY KEY(loader_id, trigger_id)
);

CREATE TABLE IF NOT EXISTS project (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    short_id TEXT NOT NULL DEFAULT '',
    source_path TEXT NOT NULL DEFAULT '',
    source_json TEXT NOT NULL DEFAULT '{}',
    current_revision INTEGER NOT NULL DEFAULT 0,
    spec_hash TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    removed_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_project_name ON project(name, removed_at);
CREATE INDEX IF NOT EXISTS idx_project_source_path ON project(source_path);
CREATE INDEX IF NOT EXISTS idx_project_short_id ON project(short_id);

CREATE TABLE IF NOT EXISTS project_revision (
    project_id TEXT NOT NULL,
    revision INTEGER NOT NULL,
    spec_hash TEXT NOT NULL,
    spec_json TEXT NOT NULL,
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    PRIMARY KEY(project_id, revision),
    FOREIGN KEY(project_id) REFERENCES project(id) ON DELETE CASCADE
);
DROP INDEX IF EXISTS idx_project_revision_hash;
CREATE INDEX idx_project_revision_hash ON project_revision(project_id, spec_hash);

CREATE TABLE IF NOT EXISTS project_agent (
    id TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    short_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL,
    agent_name TEXT NOT NULL,
    managed_agent_id TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 0,
    provider TEXT NOT NULL DEFAULT '',
    model TEXT NOT NULL DEFAULT '',
    image TEXT NOT NULL DEFAULT '',
    driver TEXT NOT NULL DEFAULT '',
    scheduler_enabled INTEGER NOT NULL DEFAULT 0,
    spec_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    PRIMARY KEY(project_id, agent_name),
    FOREIGN KEY(project_id) REFERENCES project(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_project_agent_managed_agent
    ON project_agent(managed_agent_id);
CREATE INDEX IF NOT EXISTS idx_project_agent_id ON project_agent(id);

CREATE TABLE IF NOT EXISTS project_scheduler (
    id TEXT NOT NULL DEFAULT '',
    short_id TEXT NOT NULL DEFAULT '',
    project_id TEXT NOT NULL,
    scheduler_id TEXT NOT NULL,
    agent_name TEXT NOT NULL,
    managed_loader_id TEXT NOT NULL DEFAULT '',
    revision INTEGER NOT NULL DEFAULT 0,
    enabled INTEGER NOT NULL DEFAULT 1,
    trigger_count INTEGER NOT NULL DEFAULT 0,
    spec_json TEXT NOT NULL DEFAULT '{}',
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    PRIMARY KEY(project_id, scheduler_id),
    FOREIGN KEY(project_id) REFERENCES project(id) ON DELETE CASCADE,
    FOREIGN KEY(project_id, agent_name) REFERENCES project_agent(project_id, agent_name) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_project_scheduler_agent
    ON project_scheduler(project_id, agent_name);
CREATE INDEX IF NOT EXISTS idx_project_scheduler_managed_loader
    ON project_scheduler(managed_loader_id);
CREATE INDEX IF NOT EXISTS idx_project_scheduler_id ON project_scheduler(id);

CREATE TABLE IF NOT EXISTS project_run (
    run_id TEXT PRIMARY KEY,
    project_id TEXT NOT NULL,
    project_name TEXT NOT NULL DEFAULT '',
    project_revision INTEGER NOT NULL DEFAULT 0,
    agent_name TEXT NOT NULL DEFAULT '',
    managed_agent_id TEXT NOT NULL DEFAULT '',
    source TEXT NOT NULL DEFAULT '',
    scheduler_id TEXT NOT NULL DEFAULT '',
    trigger_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL DEFAULT 'pending',
    sandbox_id TEXT NOT NULL DEFAULT '',
    exit_code INTEGER NOT NULL DEFAULT 0,
    error TEXT NOT NULL DEFAULT '',
    prompt TEXT NOT NULL DEFAULT '',
    output TEXT NOT NULL DEFAULT '',
    result_json TEXT NOT NULL DEFAULT '',
    logs_path TEXT NOT NULL DEFAULT '',
    artifacts_dir TEXT NOT NULL DEFAULT '',
    cleanup_error TEXT NOT NULL DEFAULT '',
    driver TEXT NOT NULL DEFAULT '',
    image_ref TEXT NOT NULL DEFAULT '',
    started_at INTEGER NOT NULL DEFAULT 0,
    completed_at INTEGER NOT NULL DEFAULT 0,
    duration_ms INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    FOREIGN KEY(project_id) REFERENCES project(id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_project_run_project_status
    ON project_run(project_id, status, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_project_run_agent
    ON project_run(project_id, agent_name, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_project_run_scheduler
    ON project_run(project_id, scheduler_id, trigger_id);
CREATE INDEX IF NOT EXISTS idx_project_run_sandbox ON project_run(sandbox_id);

CREATE TABLE IF NOT EXISTS project_run_event (
    id TEXT PRIMARY KEY,
    run_id TEXT NOT NULL,
    seq INTEGER NOT NULL,
    kind TEXT NOT NULL,
    text TEXT NOT NULL DEFAULT '',
    agent TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    payload_json TEXT NOT NULL DEFAULT '',
    success INTEGER NOT NULL DEFAULT 0,
    exit_code INTEGER NOT NULL DEFAULT 0,
    stop_reason TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER) * 1000),
    UNIQUE(run_id, seq),
    FOREIGN KEY(run_id) REFERENCES project_run(run_id) ON DELETE CASCADE
);
CREATE INDEX IF NOT EXISTS idx_project_run_event_sequence
    ON project_run_event(run_id, seq);

CREATE TABLE IF NOT EXISTS event (
    sequence INTEGER PRIMARY KEY AUTOINCREMENT,
    id TEXT NOT NULL UNIQUE,
    topic TEXT NOT NULL,
    source TEXT NOT NULL,
    provider TEXT NOT NULL DEFAULT '',
    intent TEXT NOT NULL DEFAULT '',
    correlation_id TEXT NOT NULL,
    idempotency_key TEXT NOT NULL DEFAULT '',
    delivery_id TEXT NOT NULL DEFAULT '',
    payload_hash TEXT NOT NULL,
    payload_json TEXT NOT NULL,
    dispatch_status TEXT NOT NULL,
    parent_event_id TEXT NOT NULL DEFAULT '',
    publisher_type TEXT NOT NULL DEFAULT '',
    publisher_id TEXT NOT NULL DEFAULT '',
    publisher_run_id TEXT NOT NULL DEFAULT '',
    replay_of_event_id TEXT NOT NULL DEFAULT '',
    claim_id TEXT NOT NULL DEFAULT '',
    claim_until INTEGER NOT NULL DEFAULT 0,
    attempt_count INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    last_error TEXT NOT NULL DEFAULT '',
    dead_letter_at INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    dispatched_at INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX IF NOT EXISTS idx_event_correlation ON event(correlation_id, sequence);
CREATE INDEX IF NOT EXISTS idx_event_topic_sequence ON event(topic, sequence);
CREATE INDEX IF NOT EXISTS idx_event_dispatch ON event(dispatch_status, sequence);
CREATE INDEX IF NOT EXISTS idx_event_dispatch_attempt
    ON event(dispatch_status, next_attempt_at, sequence);
CREATE INDEX IF NOT EXISTS idx_event_parent ON event(parent_event_id, sequence);
CREATE UNIQUE INDEX IF NOT EXISTS idx_event_idempotency
    ON event(topic, idempotency_key) WHERE idempotency_key != '';

CREATE TABLE IF NOT EXISTS webhook_source (
    id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1,
    provider TEXT NOT NULL DEFAULT '',
    topic_prefix TEXT NOT NULL,
    token_hash TEXT NOT NULL DEFAULT '',
    token_header TEXT NOT NULL DEFAULT '',
    signature_type TEXT NOT NULL DEFAULT '',
    signature_secret TEXT NOT NULL DEFAULT '',
    body_limit_bytes INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_webhook_source_enabled_topic
    ON webhook_source(enabled, topic_prefix);

CREATE TABLE IF NOT EXISTS event_delivery (
    event_id TEXT NOT NULL,
    loader_id TEXT NOT NULL,
    trigger_id TEXT NOT NULL,
    run_id TEXT NOT NULL DEFAULT '',
    status TEXT NOT NULL,
    error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY(event_id, loader_id, trigger_id)
);
CREATE INDEX IF NOT EXISTS idx_event_delivery_run ON event_delivery(run_id);
CREATE INDEX IF NOT EXISTS idx_event_delivery_status
    ON event_delivery(status, updated_at);
CREATE INDEX IF NOT EXISTS idx_event_delivery_loader_run
    ON event_delivery(loader_id, run_id);

CREATE TABLE IF NOT EXISTS event_sandbox_link (
    event_id TEXT NOT NULL,
    sandbox_id TEXT NOT NULL,
    relation TEXT NOT NULL,
    loader_id TEXT NOT NULL DEFAULT '',
    run_id TEXT NOT NULL DEFAULT '',
    trigger_id TEXT NOT NULL DEFAULT '',
    loader_event_id TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    PRIMARY KEY(event_id, sandbox_id, relation, run_id)
);
CREATE INDEX IF NOT EXISTS idx_event_sandbox_link_sandbox
    ON event_sandbox_link(sandbox_id, created_at);
CREATE INDEX IF NOT EXISTS idx_event_sandbox_link_run ON event_sandbox_link(run_id);
CREATE INDEX IF NOT EXISTS idx_event_sandbox_link_loader_run
    ON event_sandbox_link(loader_id, run_id);

CREATE TABLE IF NOT EXISTS sandbox_projection_meta (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    version INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS sandboxes (
    id                         TEXT PRIMARY KEY,
    short_id                   TEXT NOT NULL DEFAULT '',
    title                      TEXT NOT NULL DEFAULT '',
    trigger_source             TEXT NOT NULL DEFAULT '',
    driver                     TEXT NOT NULL DEFAULT '',
    vm_status                  TEXT NOT NULL DEFAULT '',
    workspace_path             TEXT NOT NULL DEFAULT '',
    workspace_id               TEXT NOT NULL DEFAULT '',
    nested_workspace_id        TEXT NOT NULL DEFAULT '',
    workspace_name             TEXT NOT NULL DEFAULT '',
    workspace_type             TEXT NOT NULL DEFAULT '',
    created_at                 INTEGER NOT NULL DEFAULT 0,
    updated_at                 INTEGER NOT NULL DEFAULT 0,
    sandbox_type               TEXT NOT NULL DEFAULT '',
    title_search               TEXT NOT NULL DEFAULT '',
    trigger_source_search      TEXT NOT NULL DEFAULT '',
    driver_search              TEXT NOT NULL DEFAULT '',
    vm_status_search           TEXT NOT NULL DEFAULT '',
    workspace_path_search      TEXT NOT NULL DEFAULT '',
    workspace_id_search        TEXT NOT NULL DEFAULT '',
    nested_workspace_id_search TEXT NOT NULL DEFAULT '',
    workspace_name_search      TEXT NOT NULL DEFAULT '',
    workspace_type_search      TEXT NOT NULL DEFAULT ''
);

CREATE INDEX IF NOT EXISTS idx_sandboxes_updated
    ON sandboxes(updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_sandboxes_vm_status_updated
    ON sandboxes(vm_status_search, updated_at DESC, id DESC);
CREATE INDEX IF NOT EXISTS idx_sandboxes_type_updated
    ON sandboxes(sandbox_type, updated_at DESC, id DESC);
