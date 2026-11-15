-- +goose Up
CREATE TABLE cash_transactions (
    id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    title VARCHAR(200) NOT NULL,
    type VARCHAR(10) NOT NULL CHECK (type IN ('inflow', 'outflow')),
    amount BIGINT NOT NULL CHECK (amount > 0),
    category VARCHAR(80) NOT NULL DEFAULT 'Lainnya',
    payment_method VARCHAR(30) NOT NULL DEFAULT 'Tunai',
    customer_name VARCHAR(120) NOT NULL DEFAULT '',
    nota_ref VARCHAR(60) NOT NULL DEFAULT '',
    has_receipt_photo BOOLEAN NOT NULL DEFAULT FALSE,
    source VARCHAR(20) NOT NULL DEFAULT 'manual' CHECK (source IN ('manual', 'voice_ai', 'receipt_ai', 'nota_sync')),
    transaction_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE INDEX idx_cash_tx_workspace_date
    ON cash_transactions(workspace_id, transaction_at DESC)
    WHERE deleted_at IS NULL;

CREATE TABLE transaction_line_items (
    id UUID PRIMARY KEY,
    transaction_id UUID NOT NULL REFERENCES cash_transactions(id) ON DELETE CASCADE,
    name VARCHAR(200) NOT NULL,
    quantity INT NOT NULL CHECK (quantity > 0),
    unit_price BIGINT NOT NULL,
    subtotal BIGINT NOT NULL,
    sort_order INT NOT NULL DEFAULT 0
);

CREATE INDEX idx_tx_line_items_tx_id ON transaction_line_items(transaction_id, sort_order ASC);

-- +goose Down
DROP TABLE IF EXISTS transaction_line_items;
DROP TABLE IF EXISTS cash_transactions;
