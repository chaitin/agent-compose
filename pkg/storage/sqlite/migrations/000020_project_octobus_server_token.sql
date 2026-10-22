-- Project-scoped OctoBus server bearer tokens. The token value lives here,
-- outside the versioned project spec (project_revision.spec_json), so rotating
-- a token never creates a new project revision. Keyed by (project_id,
-- server_name); server_name is the octobus_servers map key from the compose
-- spec.
CREATE TABLE IF NOT EXISTS project_octobus_server_token (
    project_id TEXT NOT NULL,
    server_name TEXT NOT NULL,
    token TEXT NOT NULL DEFAULT '',
    updated_at INTEGER NOT NULL DEFAULT (CAST(strftime('%s','now') AS INTEGER)),
    PRIMARY KEY(project_id, server_name)
);
