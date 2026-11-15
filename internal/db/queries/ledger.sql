-- name: GetWorkspaceMemberRole :one
SELECT role FROM workspace_members
WHERE workspace_id = $1 AND user_id = $2
LIMIT 1;

-- name: CountOwnedWorkspaces :one
SELECT COUNT(*)::INT FROM workspaces
WHERE owner_id = $1;

-- name: UpdateWorkspaceCustom :one
UPDATE workspaces
SET
    name = $2,
    short_name = $3,
    focus_type = $4,
    bank_name = $5,
    bank_account_number = $6,
    bank_account_holder = $7,
    updated_at = NOW()
WHERE id = $1
RETURNING *;

-- name: CreateCashTransaction :one
INSERT INTO cash_transactions (
    id,
    workspace_id,
    created_by,
    title,
    type,
    amount,
    category,
    payment_method,
    customer_name,
    nota_ref,
    has_receipt_photo,
    source,
    transaction_at
) VALUES (
    $1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $13
)
RETURNING *;

-- name: CreateTransactionLineItem :one
INSERT INTO transaction_line_items (
    id,
    transaction_id,
    name,
    quantity,
    unit_price,
    subtotal,
    sort_order
) VALUES (
    $1, $2, $3, $4, $5, $6, $7
)
RETURNING *;

-- name: ListCashTransactionsByWorkspace :many
SELECT * FROM cash_transactions
WHERE workspace_id = $1 AND deleted_at IS NULL
ORDER BY transaction_at DESC, created_at DESC
LIMIT $2 OFFSET $3;

-- name: ListLineItemsByWorkspaceTransactions :many
SELECT tli.*
FROM transaction_line_items tli
JOIN cash_transactions ct ON ct.id = tli.transaction_id
WHERE ct.workspace_id = $1 AND ct.deleted_at IS NULL
ORDER BY tli.sort_order ASC;

-- name: SoftDeleteCashTransaction :one
UPDATE cash_transactions
SET deleted_at = NOW(), updated_at = NOW()
WHERE id = $1 AND workspace_id = $2 AND deleted_at IS NULL
RETURNING id;
