-- Index the job history, newest first.
--
-- Until now every read of jobs went through a partial index — pending work,
-- assigned work, callbacks owed — and nothing read the table as a whole. The
-- dashboard's history does: newest first, a page at a time, resumed from the
-- last row seen.
--
-- (created_at, id) rather than created_at alone: two jobs from one fanout share
-- a transaction and therefore a now(), and a cursor on the timestamp alone
-- would either skip or repeat one of them. The id breaks the tie, and the
-- listing's row comparison (created_at, id) < (...) walks this index directly.
--
-- Not partial and not concurrent. The table only grows, so this is the one
-- index whose size tracks history; building it in the migration's transaction
-- locks writes for the duration, which at the fleet sizes V1 targets is
-- seconds at most.

-- +goose Up
CREATE INDEX jobs_created_idx ON jobs (created_at DESC, id DESC);

-- +goose Down
DROP INDEX jobs_created_idx;
