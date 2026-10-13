-- Received SMS forwarded by an automation carry it and the fields its pattern
-- extracted (amount, reference…), so a receiver does not parse the text again.

-- +goose Up
ALTER TABLE inbound_messages
    ADD COLUMN automation_id text  NOT NULL DEFAULT '',
    ADD COLUMN fields        jsonb NOT NULL DEFAULT '{}'::jsonb;

-- +goose Down
ALTER TABLE inbound_messages
    DROP COLUMN fields,
    DROP COLUMN automation_id;
