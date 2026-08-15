-- Persist per-SIM identity and health so an operator can see which
-- subscription_id to pass to POST /send, and so a dual-SIM handset's health
-- survives a restart of gatewayd.
--
-- Two JSONB columns rather than a child table: a device's SIM list is small
-- (1-2 rows), always read and written as a whole document (Register replaces
-- sim_info entirely, a heartbeat replaces sim_health entirely — there is no
-- per-SIM history, only the latest snapshot), and it is never queried by SIM
-- field. That is exactly the shape JSONB is for; a child table would only add
-- a join every device read pays for.
--
-- Default '[]', not null: every existing row and every single-SIM phone has
-- no SIMs to report, and the application already treats absence as "the
-- default subscription describes it".

-- +goose Up
ALTER TABLE devices
    ADD COLUMN sim_info jsonb NOT NULL DEFAULT '[]',
    ADD COLUMN sim_health jsonb NOT NULL DEFAULT '[]';

COMMENT ON COLUMN devices.sim_info IS
    'SimInfo[] as reported at the last Register; identity, changes only on a SIM swap.';
COMMENT ON COLUMN devices.sim_health IS
    'SimHealth[] as of the last heartbeat; replaced wholesale, no history kept.';

-- +goose Down
ALTER TABLE devices
    DROP COLUMN sim_info,
    DROP COLUMN sim_health;
