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
    logo_url = $8,
    quick_categories = $9,
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

-- name: GetWorkspaceByID :one
SELECT id, owner_id, name, short_name, focus_type, bank_name, bank_account_number, bank_account_holder, logo_url, quick_categories, is_default, created_at, updated_at
FROM workspaces
WHERE id = $1
LIMIT 1;

-- name: ListWorkspaceMembersWithProfile :many
SELECT
    wm.workspace_id,
    wm.user_id,
    wm.role,
    wm.joined_at,
    u.email,
    u.full_name,
    u.avatar_url
FROM workspace_members wm
JOIN users u ON u.id = wm.user_id
WHERE wm.workspace_id = $1
ORDER BY CASE WHEN wm.role = 'owner' THEN 0 ELSE 1 END, wm.joined_at ASC;

-- name: CountWorkspaceMembers :one
SELECT COUNT(*)::INT
FROM workspace_members
WHERE workspace_id = $1;

-- name: RemoveWorkspaceMember :execrows
DELETE FROM workspace_members
WHERE workspace_id = $1 AND user_id = $2 AND role <> 'owner';

-- name: RevokeActiveWorkspaceInvites :exec
UPDATE workspace_invites
SET revoked_at = NOW()
WHERE workspace_id = $1 AND used_by IS NULL AND revoked_at IS NULL;

-- name: CreateWorkspaceInvite :one
INSERT INTO workspace_invites (
    id, workspace_id, created_by, token, role, expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING id, workspace_id, created_by, token, role, expires_at, used_by, used_at, revoked_at, created_at;

-- name: GetActiveWorkspaceInviteByWorkspace :one
SELECT id, workspace_id, created_by, token, role, expires_at, used_by, used_at, revoked_at, created_at
FROM workspace_invites
WHERE workspace_id = $1
  AND used_by IS NULL
  AND revoked_at IS NULL
  AND expires_at > NOW()
ORDER BY created_at DESC
LIMIT 1;

-- name: GetWorkspaceInviteByTokenForUpdate :one
SELECT id, workspace_id, created_by, token, role, expires_at, used_by, used_at, revoked_at, created_at
FROM workspace_invites
WHERE token = $1
LIMIT 1
FOR UPDATE;

-- name: MarkWorkspaceInviteUsed :one
UPDATE workspace_invites
SET used_by = $2, used_at = NOW()
WHERE id = $1 AND used_by IS NULL AND revoked_at IS NULL
RETURNING id, workspace_id, created_by, token, role, expires_at, used_by, used_at, revoked_at, created_at;

-- name: ListWorkspaceAuditLogsWithUser :many
SELECT
    al.id,
    al.user_id,
    al.workspace_id,
    al.action,
    al.ip_address,
    al.metadata_json,
    al.created_at,
    COALESCE(u.full_name, 'Pengguna') AS actor_name,
    COALESCE(u.email, '') AS actor_email,
    COALESCE(u.avatar_url, '') AS actor_avatar_url
FROM audit_logs al
LEFT JOIN users u ON u.id = al.user_id
WHERE al.workspace_id = $1
ORDER BY al.created_at DESC
LIMIT $2 OFFSET $3;
