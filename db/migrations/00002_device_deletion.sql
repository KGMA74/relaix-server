-- Let a device be deleted without breaking the enrollment audit trail.
--
-- devices can now be retired through DELETE /devices/:id. Every table that
-- points at a device already used ON DELETE SET NULL, so the history of what a
-- phone did outlives the phone — except that enrollment_tokens carried a
-- constraint the deletion could not satisfy:
--
--     CHECK ((consumed_at IS NULL) = (device_id IS NULL))
--
-- It read as "consumed if and only if a device exists". Deleting a device
-- nulls device_id while consumed_at stays set, which is the one state that
-- equivalence forbids, so the DELETE failed with a check violation.
--
-- The invariant worth keeping is weaker and one-directional: a token that
-- names a device must have been consumed. The reverse is no longer true —
-- "consumed by a device that has since been retired" is a legitimate state,
-- and refusing to represent it was what made devices undeletable.
--
-- Relaxing rather than dropping matters: without any check, a row could claim
-- a device it never enrolled.

-- +goose Up
ALTER TABLE enrollment_tokens
    DROP CONSTRAINT enrollment_tokens_consumed_check;

ALTER TABLE enrollment_tokens
    ADD CONSTRAINT enrollment_tokens_consumed_check
        CHECK (device_id IS NULL OR consumed_at IS NOT NULL);

-- +goose Down
-- Restoring the equivalence can fail on real data: any token whose device was
-- deleted violates it. Rows in that state are stripped back to unconsumed
-- first, which is safe because the token hash stays unique and a token with no
-- consumed_at is still refused by Consume once its expiry has passed.
UPDATE enrollment_tokens
SET consumed_at = NULL
WHERE device_id IS NULL
  AND consumed_at IS NOT NULL;

ALTER TABLE enrollment_tokens
    DROP CONSTRAINT enrollment_tokens_consumed_check;

ALTER TABLE enrollment_tokens
    ADD CONSTRAINT enrollment_tokens_consumed_check
        CHECK ((consumed_at IS NULL) = (device_id IS NULL));
