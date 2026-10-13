-- USSD jobs, beside SMS ones.
--
-- A USSD job is a job: one SIM of one phone runs it, it is acknowledged,
-- retried, cancelled, called back and kept in the history exactly like an SMS,
-- so it reuses the table rather than duplicating every part of that machinery.
-- kind tells them apart; for a USSD job, body holds the code dialed and
-- recipient stays empty.
--
-- replies are typed into the menus the code opens, in order; responses are
-- what the network displayed, one per screen. sensitive marks a job whose
-- code or replies carry a secret (a PIN): the API never shows them, and both
-- are wiped once the job is over, so the database does not keep them.
--
-- capabilities is what each agent says it can do beyond SMS, so a job is
-- never handed to one that would drop it.

-- +goose Up
ALTER TABLE jobs
    ADD COLUMN kind      text    NOT NULL DEFAULT 'sms',
    ADD COLUMN replies   text[]  NOT NULL DEFAULT '{}',
    ADD COLUMN responses text[]  NOT NULL DEFAULT '{}',
    ADD COLUMN sensitive boolean NOT NULL DEFAULT false,
    ADD CONSTRAINT jobs_kind_check CHECK (kind IN ('sms', 'ussd'));

ALTER TABLE devices
    ADD COLUMN capabilities text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE devices DROP COLUMN capabilities;
ALTER TABLE jobs
    DROP CONSTRAINT jobs_kind_check,
    DROP COLUMN sensitive,
    DROP COLUMN responses,
    DROP COLUMN replies,
    DROP COLUMN kind;
