-- Initial schema for the Relaix control plane.
--
-- Shape and rationale: docs/architecture.md §8 in the monorepo. Field meanings
-- that mirror the wire contract are documented in docs/protocol.md.
--
-- Conventions used throughout:
--   * uuid primary keys via gen_random_uuid(), built into Postgres since 13 —
--     no pgcrypto extension needed.
--   * timestamptz everywhere, never timestamp: the fleet is portable and the
--     server may not share a time zone with any of it.
--   * status/mode columns are text with a CHECK constraint rather than native
--     enum types. Adding a value later is a one-line constraint change instead
--     of an ALTER TYPE that cannot run in a transaction with other DDL.
--   * secrets are stored hashed, never in plaintext, so a database dump does
--     not hand over the fleet.

-- +goose Up

-- ---------------------------------------------------------------------------
-- devices
-- ---------------------------------------------------------------------------

CREATE TABLE devices (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Operator-facing name ("desk phone", "SIM Orange 2").
    label               text        NOT NULL DEFAULT '',

    -- The device's own MSISDN in E.164. This is the number recipients see;
    -- there is no custom sender ID. May be empty: Android cannot always read it
    -- from the SIM.
    phone_number        text        NOT NULL DEFAULT '',

    -- Hash of the long-lived device token issued at enrollment. The plaintext
    -- is returned to the agent exactly once and never stored.
    token_hash          text        NOT NULL,

    -- Operator kill switch. A disabled device is refused at Register and is
    -- never selected by the scheduler, without deleting its history.
    enabled             boolean     NOT NULL DEFAULT true,

    -- Descriptive fields from DeviceInfo, refreshed on every Register so the
    -- server's view survives OS updates, app updates and SIM swaps.
    manufacturer        text        NOT NULL DEFAULT '',
    model               text        NOT NULL DEFAULT '',
    os_version          text        NOT NULL DEFAULT '',
    agent_version       text        NOT NULL DEFAULT '',
    carrier             text        NOT NULL DEFAULT '',

    -- Last DeviceHealth snapshot. All nullable: a device that has enrolled but
    -- never connected has no health to report, and NULL says that honestly
    -- where a zero would read as "battery empty, no signal".
    battery_level       smallint,
    is_charging         boolean,
    signal_strength     smallint,
    network_type        text,
    sim_ready           boolean,
    sent_last_hour      integer,
    permissions_ok      boolean,
    health_reported_at  timestamptz,

    -- Liveness. Refreshed on every heartbeat; the hub treats a device whose
    -- last_seen_at has gone stale as gone, even if its socket still looks open.
    last_seen_at        timestamptz,

    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),

    -- Normalized 0-4 level, not raw dBm — see docs/protocol.md. Constrained
    -- here so a miscalibrated agent cannot poison scheduler decisions.
    CONSTRAINT devices_signal_strength_range
        CHECK (signal_strength IS NULL OR signal_strength BETWEEN 0 AND 4),
    CONSTRAINT devices_battery_level_range
        CHECK (battery_level IS NULL OR battery_level BETWEEN 0 AND 100)
);

-- Enforces that one token identifies at most one device, and makes token
-- lookup on every single stream message an index hit rather than a scan.
CREATE UNIQUE INDEX devices_token_hash_key ON devices (token_hash);

-- ---------------------------------------------------------------------------
-- jobs
-- ---------------------------------------------------------------------------

CREATE TABLE jobs (
    id                  uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    recipient           text        NOT NULL,
    body                text        NOT NULL,

    -- immediate: fail fast if no device can take it right now.
    -- queued:    persist and retry on later ticks.
    mode                text        NOT NULL,

    -- Higher is considered first. Ordering, not preemption: a job already
    -- handed to a device is never recalled because something more urgent came.
    priority            integer     NOT NULL DEFAULT 0,

    -- Hold the job back until this time. NULL means eligible immediately.
    scheduled_at        timestamptz,

    -- After this, the job must not be sent. Stops a phone that was offline for
    -- hours from delivering a stale OTP on reconnect.
    expires_at          timestamptz,

    -- Device the caller explicitly asked for, if any. Kept separate from
    -- assigned_device_id because they answer different questions: what was
    -- requested, versus what actually happened. A requested device is never
    -- silently substituted.
    requested_device_id uuid REFERENCES devices (id) ON DELETE SET NULL,
    assigned_device_id  uuid REFERENCES devices (id) ON DELETE SET NULL,

    status              text        NOT NULL DEFAULT 'pending',

    -- Outcome, mirroring JobResult.
    error_code          text        NOT NULL DEFAULT '',
    error_message       text        NOT NULL DEFAULT '',

    -- Number of SMS parts actually sent. A long or non-GSM-7 body is split into
    -- several billed messages, so this is what callers reconcile cost against.
    parts_sent          integer     NOT NULL DEFAULT 0,

    -- How many times this job has been dispatched to a device. Delivery is
    -- at-least-once, so a redispatch after a reconnect is normal; this bounds
    -- it so a job that no device can stomach cannot loop forever.
    attempts            integer     NOT NULL DEFAULT 0,

    -- Callback delivery state. Held on the job rather than in its own table:
    -- there is exactly one callback per job, and the watcher's poll is then a
    -- single indexed scan with no join.
    callback_url          text        NOT NULL DEFAULT '',
    callback_attempts     integer     NOT NULL DEFAULT 0,
    callback_next_at      timestamptz,
    callback_delivered_at timestamptz,
    callback_last_error   text        NOT NULL DEFAULT '',

    created_at          timestamptz NOT NULL DEFAULT now(),
    updated_at          timestamptz NOT NULL DEFAULT now(),
    assigned_at         timestamptz,
    completed_at        timestamptz,

    CONSTRAINT jobs_mode_check
        CHECK (mode IN ('immediate', 'queued')),
    CONSTRAINT jobs_status_check
        CHECK (status IN ('pending', 'assigned', 'sent', 'delivered', 'failed', 'cancelled'))
);

-- The scheduler's hot path: on every tick, find eligible work in priority
-- order. Partial, because pending is a small and shrinking slice of a table
-- that only grows — the index stays small however much history accumulates.
CREATE INDEX jobs_pending_idx
    ON jobs (priority DESC, scheduled_at NULLS FIRST, created_at)
    WHERE status = 'pending';

-- Reconciliation after a reconnect: what does this device still owe us.
CREATE INDEX jobs_assigned_device_idx
    ON jobs (assigned_device_id)
    WHERE status = 'assigned';

-- The callback watcher's poll: jobs whose webhook is still owed and due.
CREATE INDEX jobs_callback_due_idx
    ON jobs (callback_next_at)
    WHERE callback_delivered_at IS NULL AND callback_url <> '';

-- ---------------------------------------------------------------------------
-- enrollment_tokens
-- ---------------------------------------------------------------------------

CREATE TABLE enrollment_tokens (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),

    -- Hash of the single-use token encoded in the QR code.
    token_hash   text        NOT NULL,

    expires_at   timestamptz NOT NULL,

    -- Set when the token is redeemed. The consumption is what makes a
    -- photographed QR code worthless after the first enrollment, so this column
    -- is the enforcement point: claiming a token is an UPDATE that requires
    -- consumed_at IS NULL and must affect exactly one row.
    consumed_at  timestamptz,

    -- The device this token created, once redeemed. Gives an audit trail from
    -- "who minted a token" to "which phone joined".
    device_id    uuid REFERENCES devices (id) ON DELETE SET NULL,

    created_at   timestamptz NOT NULL DEFAULT now(),

    -- A consumed token must point at the device it created, and an unconsumed
    -- one must point at nothing. Keeps the two columns from drifting apart.
    CONSTRAINT enrollment_tokens_consumed_check
        CHECK ((consumed_at IS NULL) = (device_id IS NULL))
);

CREATE UNIQUE INDEX enrollment_tokens_token_hash_key
    ON enrollment_tokens (token_hash);

-- Sweeping expired, never-redeemed tokens.
CREATE INDEX enrollment_tokens_pending_idx
    ON enrollment_tokens (expires_at)
    WHERE consumed_at IS NULL;

-- ---------------------------------------------------------------------------
-- job_events
-- ---------------------------------------------------------------------------

-- Append-only audit trail. Separate from jobs because jobs carries current
-- state and is updated in place, while this is never mutated: it answers why a
-- message ended up where it did — which device took it, when it was retried,
-- what the failure was — without complicating the row the scheduler reads on
-- every tick.
CREATE TABLE job_events (
    id         bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,

    job_id     uuid        NOT NULL REFERENCES jobs (id) ON DELETE CASCADE,

    -- Status the job moved into, or NULL for an event that records something
    -- other than a transition (a retry, a callback attempt).
    status     text,

    -- Device involved, when the event is about one. Not a foreign key on
    -- purpose: the log must survive the deletion of a device it mentions.
    device_id  uuid,

    -- Human-readable explanation, e.g. "no ready device", "carrier error 38".
    reason     text        NOT NULL DEFAULT '',

    created_at timestamptz NOT NULL DEFAULT now()
);

-- Reading one job's history in order.
CREATE INDEX job_events_job_idx ON job_events (job_id, created_at);

-- +goose Down

DROP TABLE IF EXISTS job_events;
DROP TABLE IF EXISTS enrollment_tokens;
DROP TABLE IF EXISTS jobs;
DROP TABLE IF EXISTS devices;
