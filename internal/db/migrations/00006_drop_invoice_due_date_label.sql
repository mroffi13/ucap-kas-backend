-- +goose Up
ALTER TABLE invoices
    ADD COLUMN IF NOT EXISTS due_at TIMESTAMPTZ;

UPDATE invoices
SET due_at = issued_at + INTERVAL '7 days'
WHERE due_at IS NULL;

ALTER TABLE invoices
    ALTER COLUMN due_at SET NOT NULL,
    ALTER COLUMN due_at SET DEFAULT (NOW() + INTERVAL '7 days'),
    DROP COLUMN IF EXISTS due_date_label;

-- +goose Down
ALTER TABLE invoices
    ADD COLUMN IF NOT EXISTS due_date_label VARCHAR(80) NOT NULL DEFAULT '';
