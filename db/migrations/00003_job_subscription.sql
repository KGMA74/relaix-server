-- Let a caller pick which SIM a message leaves from.
--
-- A dual-SIM handset is two sending nodes wearing one device row: the carrier
-- meters each SIM separately, and the number a recipient sees is the SIM's,
-- not the phone's. Until now a job went out on whatever Android had set as the
-- default SMS subscription, which is a setting on the handset and invisible
-- from here.
--
-- Nullable, and null keeps meaning "the device's default": every existing row
-- was written under that rule and every single-SIM phone stays on it.
--
-- No foreign key and no check against a list of known SIMs. Subscription ids
-- are handed out by Android per handset, so the same integer means different
-- SIMs on two phones and the value is only interpretable next to a device. The
-- agent refuses a subscription it does not have, which is the only place that
-- can tell.

-- +goose Up
ALTER TABLE jobs
    ADD COLUMN subscription_id integer;

COMMENT ON COLUMN jobs.subscription_id IS
    'Android subscription id of the SIM to send from; null means the device default.';

-- +goose Down
ALTER TABLE jobs
    DROP COLUMN subscription_id;
