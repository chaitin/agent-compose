-- The Engine Project whose scheduler published an event.
--
-- Event delivery scope is decided against the publisher's Project when the
-- dispatcher hands a stored event to the scheduler bus, which may happen after
-- a restart or a retry, so the attribution is stored with the event rather
-- than looked up from a scheduler that may have changed since. Daemon-level
-- publishers such as webhook ingress have no publisher Project.
ALTER TABLE event ADD COLUMN publisher_project_id TEXT NOT NULL DEFAULT '';

-- Attribute events stored before this column existed to their publishing
-- scheduler's Project, so an event still waiting for dispatch at upgrade time
-- reaches that Project's subscribers instead of being acknowledged as having
-- none. An event whose scheduler is gone keeps no Project.
UPDATE event
SET publisher_project_id = COALESCE(
    (SELECT project_scheduler.project_id FROM project_scheduler WHERE project_scheduler.id = event.publisher_id),
    ''
)
WHERE publisher_type = 'scheduler' AND publisher_id <> '';
