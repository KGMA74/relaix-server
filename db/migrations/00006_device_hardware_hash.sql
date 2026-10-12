-- One physical phone, one device.
--
-- Until now every enrollment created a device, so a phone reinstalled, reset
-- or re-enrolled after a lost token joined the fleet a second time and left
-- its first row behind as a phone that would never reconnect. The agent now
-- sends a stable hardware id on Enroll; the server keeps an HMAC of it here
-- and, when it sees the same one again, re-keys that row instead of adding
-- one.
--
-- Nullable, and unique only where set: devices enrolled by older agents have
-- no hash, and must not collide with one another over it.

-- +goose Up
ALTER TABLE devices ADD COLUMN hardware_hash text;
CREATE UNIQUE INDEX devices_hardware_hash_key ON devices (hardware_hash) WHERE hardware_hash IS NOT NULL;

-- +goose Down
DROP INDEX devices_hardware_hash_key;
ALTER TABLE devices DROP COLUMN hardware_hash;
