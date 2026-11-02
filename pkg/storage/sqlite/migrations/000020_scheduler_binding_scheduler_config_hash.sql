ALTER TABLE scheduler_sandbox_binding
    ADD COLUMN scheduler_config_hash TEXT NOT NULL DEFAULT '';
