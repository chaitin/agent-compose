-- The Engine Project whose scheduler published an event.
--
-- Event delivery scope is decided against the publisher's Project when the
-- dispatcher hands a stored event to the scheduler bus, which may happen after
-- a restart or a retry, so the attribution is stored with the event rather
-- than looked up from a scheduler that may have changed since. Events stored
-- before this column existed, and daemon-level publishers such as webhook
-- ingress, have no publisher Project.
ALTER TABLE event ADD COLUMN publisher_project_id TEXT NOT NULL DEFAULT '';
