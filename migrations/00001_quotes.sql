-- +goose Up
CREATE TABLE quotes (
    id           uuid PRIMARY KEY,
    payment_id   text,
    base_asset   text        NOT NULL,
    quote_asset  text        NOT NULL,
    rate         text        NOT NULL,
    locked_until timestamptz NOT NULL,
    created_at   timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX quotes_payment_idx ON quotes (payment_id);
CREATE INDEX quotes_pair_idx ON quotes (base_asset, quote_asset, created_at);

-- +goose Down
DROP TABLE IF EXISTS quotes;
