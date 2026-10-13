-- What the phones' automations did, as they report it once each run is over.
--
-- One row per device and run id (device_ref), stable across the resends that
-- follow a lost acknowledgement. steps is the per-action outcome, read and
-- written whole. Kept after the device is deleted, like jobs and inbound
-- messages.

-- +goose Up
CREATE TABLE automation_runs (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id    uuid NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
    device_id     uuid REFERENCES devices (id) ON DELETE SET NULL,
    device_ref    text NOT NULL,
    automation_id text NOT NULL,
    sender        text NOT NULL DEFAULT '',
    started_at    timestamptz NOT NULL,
    finished_at   timestamptz NOT NULL,
    ok            boolean NOT NULL,
    error         text NOT NULL DEFAULT '',
    steps         jsonb NOT NULL DEFAULT '[]'::jsonb,
    created_at    timestamptz NOT NULL DEFAULT now()
);

CREATE UNIQUE INDEX automation_runs_device_ref_key ON automation_runs (device_id, device_ref);
CREATE INDEX automation_runs_account_started_idx ON automation_runs (account_id, started_at DESC, id DESC);

-- +goose Down
DROP TABLE automation_runs;
