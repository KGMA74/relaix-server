-- Each device's rules: when it takes work and for which recipients, per SIM
-- too (package policy).
--
-- One jsonb document rather than tables of windows and countries. The rules
-- are always read and written whole — the scheduler evaluates a device's
-- policy in one piece, both editors replace it in one piece — and nothing
-- queries inside them. The empty document is the zero policy: no restriction,
-- which is what every existing device keeps.
--
-- time_zone is the zone the phone reports, which windows default to.

-- +goose Up
ALTER TABLE devices
    ADD COLUMN policy jsonb NOT NULL DEFAULT '{}'::jsonb,
    ADD COLUMN time_zone text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE devices
    DROP COLUMN policy,
    DROP COLUMN time_zone;
