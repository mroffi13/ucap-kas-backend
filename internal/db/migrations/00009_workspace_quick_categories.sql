-- +goose Up
ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS quick_categories JSONB NOT NULL DEFAULT '["Servis", "Bahan Baku", "Operasional"]'::jsonb;

-- +goose Down
ALTER TABLE workspaces DROP COLUMN IF EXISTS quick_categories;
