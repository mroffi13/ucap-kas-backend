-- +goose Up
-- +goose StatementBegin
CREATE TABLE ai_usage_logs (
    id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    feature_type VARCHAR(20) NOT NULL CHECK (feature_type IN ('voice_cash', 'voice_invoice', 'receipt_ocr')),
    provider VARCHAR(20) NOT NULL DEFAULT 'gemini_flash',
    tokens_used INT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_ai_usage_user_month ON ai_usage_logs(user_id, created_at DESC);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS ai_usage_logs;
-- +goose StatementEnd
