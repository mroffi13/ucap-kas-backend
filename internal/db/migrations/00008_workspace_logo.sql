-- +goose Up
ALTER TABLE workspaces ADD COLUMN IF NOT EXISTS logo_url TEXT NOT NULL DEFAULT '';

-- +goose Down
ALTER TABLE workspaces DROP COLUMN IF EXISTS logo_url;
