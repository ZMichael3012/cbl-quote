-- +goose Up
-- Provider pair minimums (17.10): the minimum amount that was applied to the
-- lock request, in minor units of the request amount asset, for audit.
ALTER TABLE quotes
    ADD COLUMN min_amount_minor bigint CHECK (min_amount_minor IS NULL OR min_amount_minor > 0);

-- +goose Down
ALTER TABLE quotes
    DROP COLUMN IF EXISTS min_amount_minor;
