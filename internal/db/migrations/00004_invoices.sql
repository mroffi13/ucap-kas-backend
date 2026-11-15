-- +goose Up
CREATE TABLE invoices (
    id UUID PRIMARY KEY,
    workspace_id UUID NOT NULL REFERENCES workspaces(id) ON DELETE CASCADE,
    created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    invoice_number VARCHAR(60) NOT NULL,
    client_name VARCHAR(160) NOT NULL,
    client_contact VARCHAR(120) NOT NULL DEFAULT '',
    subtitle VARCHAR(200) NOT NULL DEFAULT '',
    due_at TIMESTAMPTZ NOT NULL DEFAULT (NOW() + INTERVAL '7 days'),
    amount BIGINT NOT NULL CHECK (amount > 0),
    status VARCHAR(20) NOT NULL DEFAULT 'menunggu' CHECK (status IN ('menunggu', 'jatuh_tempo', 'lunas')),
    notes VARCHAR(500) NOT NULL DEFAULT '',
    issued_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    paid_at TIMESTAMPTZ,
    paid_transaction_id UUID REFERENCES cash_transactions(id) ON DELETE SET NULL,
    deleted_at TIMESTAMPTZ,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_invoices_ws_number
    ON invoices(workspace_id, invoice_number)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_invoices_workspace_created
    ON invoices(workspace_id, issued_at DESC, created_at DESC)
    WHERE deleted_at IS NULL;

CREATE INDEX idx_invoices_due_at
    ON invoices(workspace_id, due_at)
    WHERE deleted_at IS NULL AND status = 'menunggu';

CREATE TABLE invoice_line_items (
    id UUID PRIMARY KEY,
    invoice_id UUID NOT NULL REFERENCES invoices(id) ON DELETE CASCADE,
    name VARCHAR(200) NOT NULL,
    quantity INT NOT NULL CHECK (quantity > 0),
    unit_price BIGINT NOT NULL,
    subtotal BIGINT NOT NULL,
    sort_order INT NOT NULL DEFAULT 0
);

CREATE INDEX idx_invoice_line_items_inv_id ON invoice_line_items(invoice_id, sort_order ASC);

-- +goose Down
DROP TABLE IF EXISTS invoice_line_items;
DROP TABLE IF EXISTS invoices;
