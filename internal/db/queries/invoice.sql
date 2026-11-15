-- name: CountInvoicesByWorkspace :one
SELECT COUNT(*)::INT FROM invoices
WHERE workspace_id = $1;

-- name: CreateInvoice :one
INSERT INTO invoices (
    id,
    workspace_id,
    created_by,
    invoice_number,
    client_name,
    client_contact,
    subtitle,
    due_at,
    amount,
    status,
    notes,
    issued_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12
)
RETURNING *;

-- name: CreateInvoiceLineItem :one
INSERT INTO invoice_line_items (
    id,
    invoice_id,
    name,
    quantity,
    unit_price,
    subtotal,
    sort_order
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;

-- name: ListInvoicesByWorkspace :many
SELECT * FROM invoices
WHERE workspace_id = $1
  AND deleted_at IS NULL
  AND ($2::TEXT = '' OR status = $2::TEXT)
ORDER BY issued_at DESC, created_at DESC
LIMIT $3 OFFSET $4;

-- name: ListLineItemsByWorkspaceInvoices :many
SELECT ili.*
FROM invoice_line_items ili
JOIN invoices inv ON inv.id = ili.invoice_id
WHERE inv.workspace_id = $1 AND inv.deleted_at IS NULL
ORDER BY ili.sort_order ASC;

-- name: ListLineItemsByInvoiceID :many
SELECT * FROM invoice_line_items
WHERE invoice_id = $1
ORDER BY sort_order ASC;

-- name: GetInvoiceByIDForUpdate :one
SELECT * FROM invoices
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL
FOR UPDATE;

-- name: MarkInvoicePaid :one
UPDATE invoices
SET
    status = 'lunas',
    subtitle = $3,
    paid_at = NOW(),
    paid_transaction_id = $4,
    updated_at = NOW()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: UpdateInvoice :one
UPDATE invoices
SET
    client_name = $3,
    client_contact = $4,
    subtitle = $5,
    due_at = $6,
    status = $7,
    notes = $8,
    updated_at = NOW()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL
RETURNING *;

-- name: SoftDeleteInvoice :one
UPDATE invoices
SET deleted_at = NOW(), updated_at = NOW()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL
RETURNING id;
