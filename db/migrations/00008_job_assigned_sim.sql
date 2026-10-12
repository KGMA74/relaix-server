-- Which SIM a job was handed to, and what send limits count on.
--
-- jobs.subscription_id is the caller's request and must stay so: a job
-- released back to pending and placed on another phone cannot carry the first
-- phone's SIM id. The scheduler's choice — the requested SIM, or the one the
-- device's rules picked — goes here instead, set with assigned_device_id and
-- cleared with it. NULL: the phone's default SIM, not named.
--
-- The index serves the per-tick count behind send limits: messages handed to
-- each device (and SIM) over the last hour and day. Partial, because only
-- assigned rows are ever counted, and those are a sliver of the table.

-- +goose Up
ALTER TABLE jobs ADD COLUMN assigned_subscription_id integer;
CREATE INDEX jobs_assigned_recent_idx ON jobs (assigned_at, assigned_device_id)
    WHERE assigned_at IS NOT NULL;

-- +goose Down
DROP INDEX jobs_assigned_recent_idx;
ALTER TABLE jobs DROP COLUMN assigned_subscription_id;
