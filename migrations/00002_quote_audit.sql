-- +goose Up
ALTER TABLE quotes
    ADD COLUMN as_of          timestamptz,
    ADD COLUMN ttl_seconds    integer,
    ADD COLUMN amount_asset   text,
    ADD COLUMN amount_minor   bigint,
    ADD COLUMN sources        jsonb,
    ADD COLUMN spread         text,
    ADD COLUMN guard_adjusted boolean NOT NULL DEFAULT false;

ALTER TABLE quotes
    ADD CONSTRAINT quotes_ttl_positive CHECK (ttl_seconds IS NULL OR ttl_seconds > 0),
    ADD CONSTRAINT quotes_amount_positive CHECK (amount_minor IS NULL OR amount_minor > 0);

CREATE INDEX quotes_created_at_idx ON quotes (created_at, id);

-- +goose StatementBegin
CREATE FUNCTION quotes_append_only() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'quotes is append-only: % is forbidden', TG_OP;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp;
-- +goose StatementEnd

CREATE TRIGGER quotes_append_only_trigger
    BEFORE UPDATE OR DELETE ON quotes
    FOR EACH ROW EXECUTE FUNCTION quotes_append_only();

-- +goose StatementBegin
CREATE FUNCTION quotes_no_truncate() RETURNS trigger AS $$
BEGIN
    RAISE EXCEPTION 'quotes is append-only: TRUNCATE is forbidden';
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, pg_temp;
-- +goose StatementEnd

CREATE TRIGGER quotes_no_truncate_trigger
    BEFORE TRUNCATE ON quotes
    FOR EACH STATEMENT EXECUTE FUNCTION quotes_no_truncate();

-- +goose Down
DROP TRIGGER IF EXISTS quotes_no_truncate_trigger ON quotes;
DROP FUNCTION IF EXISTS quotes_no_truncate();
DROP TRIGGER IF EXISTS quotes_append_only_trigger ON quotes;
DROP FUNCTION IF EXISTS quotes_append_only();
DROP INDEX IF EXISTS quotes_created_at_idx;
ALTER TABLE quotes
    DROP CONSTRAINT IF EXISTS quotes_ttl_positive,
    DROP CONSTRAINT IF EXISTS quotes_amount_positive,
    DROP COLUMN IF EXISTS as_of,
    DROP COLUMN IF EXISTS ttl_seconds,
    DROP COLUMN IF EXISTS amount_asset,
    DROP COLUMN IF EXISTS amount_minor,
    DROP COLUMN IF EXISTS sources,
    DROP COLUMN IF EXISTS spread,
    DROP COLUMN IF EXISTS guard_adjusted;
