-- Accounts: who devices, jobs and enrollment tokens belong to.
--
-- Until now everything belonged to nobody, which is right for one operator
-- and wrong as soon as two share a gateway: each must see, and send through,
-- only its own phones. An account here is deliberately minimal — an id and a
-- name, a scope owner. Whatever else an account might carry (people, keys,
-- plans) is for the program embedding the gateway to add, keyed on this id.
--
-- Existing rows move to a default account with a well-known id, so a
-- single-operator deployment keeps working exactly as before: the shared API
-- key acts on that account, and nothing it can see changes.
--
-- The column defaults exist only to backfill; they are dropped right after,
-- so a write that forgets the account fails loudly instead of quietly landing
-- in the default one.
--
-- History listings now filter by account first, so jobs_created_idx gains an
-- account-leading twin; the account-less one stays for cross-account reads.

-- +goose Up
CREATE TABLE accounts (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text NOT NULL,
    created_at timestamptz NOT NULL DEFAULT now()
);

INSERT INTO accounts (id, name) VALUES ('00000000-0000-0000-0000-000000000001', 'default');

ALTER TABLE devices
    ADD COLUMN account_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000001'
        REFERENCES accounts (id) ON DELETE RESTRICT;
ALTER TABLE devices ALTER COLUMN account_id DROP DEFAULT;

ALTER TABLE jobs
    ADD COLUMN account_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000001'
        REFERENCES accounts (id) ON DELETE RESTRICT;
ALTER TABLE jobs ALTER COLUMN account_id DROP DEFAULT;

ALTER TABLE enrollment_tokens
    ADD COLUMN account_id uuid NOT NULL DEFAULT '00000000-0000-0000-0000-000000000001'
        REFERENCES accounts (id) ON DELETE RESTRICT;
ALTER TABLE enrollment_tokens ALTER COLUMN account_id DROP DEFAULT;

CREATE INDEX devices_account_idx ON devices (account_id);
CREATE INDEX jobs_account_created_idx ON jobs (account_id, created_at DESC, id DESC);

-- +goose Down
DROP INDEX jobs_account_created_idx;
DROP INDEX devices_account_idx;
ALTER TABLE enrollment_tokens DROP COLUMN account_id;
ALTER TABLE jobs DROP COLUMN account_id;
ALTER TABLE devices DROP COLUMN account_id;
DROP TABLE accounts;
