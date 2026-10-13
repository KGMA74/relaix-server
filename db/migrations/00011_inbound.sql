-- SMS received by the phones, forwarded under their rules (policy.inbound).
--
-- One row per message and device: device_ref is the id the agent gave it,
-- stable across the resends that follow a lost acknowledgement, so a message
-- is recorded once however often it arrives.
--
-- The webhook fields mirror the jobs' callback ones, for the same reasons:
-- one webhook per message, retried with backoff by a watcher that claims due
-- rows with SKIP LOCKED. webhook_url is resolved when the message is
-- recorded, so changing the configuration later does not re-send history.
--
-- device_id survives the device as NULL, like on jobs: retiring a phone must
-- not erase what it received. The unique index then no longer constrains the
-- orphans, which is fine — their phone will send nothing more.

-- +goose Up
CREATE TABLE inbound_messages (
    id                   uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    account_id           uuid NOT NULL REFERENCES accounts (id) ON DELETE RESTRICT,
    device_id            uuid REFERENCES devices (id) ON DELETE SET NULL,
    device_ref           text NOT NULL,
    subscription_id      integer,
    sender               text NOT NULL,
    body                 text NOT NULL,
    received_at          timestamptz NOT NULL,
    created_at           timestamptz NOT NULL DEFAULT now(),

    webhook_url          text NOT NULL DEFAULT '',
    webhook_attempts     integer NOT NULL DEFAULT 0,
    webhook_next_at      timestamptz,
    webhook_delivered_at timestamptz,
    webhook_last_error   text NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX inbound_messages_device_ref_key ON inbound_messages (device_id, device_ref);
CREATE INDEX inbound_messages_account_received_idx ON inbound_messages (account_id, received_at DESC, id DESC);
CREATE INDEX inbound_messages_webhook_due_idx ON inbound_messages (webhook_next_at)
    WHERE webhook_delivered_at IS NULL AND webhook_next_at IS NOT NULL;

-- +goose Down
DROP TABLE inbound_messages;
