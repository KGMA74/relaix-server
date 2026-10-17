-- +goose Up

-- A USSD job may follow an answer over several screens ("n Suivant") and
-- extract fields from what the network displayed. paging is the JSON of
-- policy.Paging, NULL for none; extract a regular expression, '' for none.
ALTER TABLE jobs
    ADD COLUMN paging jsonb,
    ADD COLUMN extract text NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE jobs
    DROP COLUMN extract,
    DROP COLUMN paging;
