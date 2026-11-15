-- name: UpsertUserByGoogleSub :one
INSERT INTO users (
    id, google_sub, email, full_name, avatar_url
) VALUES (
    $1, $2, $3, $4, $5
)
ON CONFLICT (google_sub) DO UPDATE SET
    email = EXCLUDED.email,
    full_name = EXCLUDED.full_name,
    avatar_url = CASE WHEN EXCLUDED.avatar_url <> '' THEN EXCLUDED.avatar_url ELSE users.avatar_url END,
    updated_at = NOW()
RETURNING *;

-- name: GetUserByID :one
SELECT * FROM users WHERE id = $1 LIMIT 1;

-- name: ListWorkspacesByUserID :many
SELECT
    w.id,
    w.owner_id,
    w.name,
    w.short_name,
    w.focus_type,
    w.bank_name,
    w.bank_account_number,
    w.bank_account_holder,
    w.is_default,
    wm.role,
    COALESCE(SUM(CASE
        WHEN ct.id IS NOT NULL AND ct.deleted_at IS NULL AND ct.type = 'inflow' THEN ct.amount
        WHEN ct.id IS NOT NULL AND ct.deleted_at IS NULL AND ct.type = 'outflow' THEN -ct.amount
        ELSE 0
    END), 0)::BIGINT AS balance,
    COALESCE(SUM(CASE
        WHEN ct.id IS NOT NULL AND ct.deleted_at IS NULL AND ct.type = 'inflow'
             AND date_trunc('month', ct.transaction_at AT TIME ZONE 'Asia/Jakarta') = date_trunc('month', NOW() AT TIME ZONE 'Asia/Jakarta')
        THEN ct.amount
        ELSE 0
    END), 0)::BIGINT AS monthly_inflow,
    COALESCE(SUM(CASE
        WHEN ct.id IS NOT NULL AND ct.deleted_at IS NULL AND ct.type = 'outflow'
             AND date_trunc('month', ct.transaction_at AT TIME ZONE 'Asia/Jakarta') = date_trunc('month', NOW() AT TIME ZONE 'Asia/Jakarta')
        THEN ct.amount
        ELSE 0
    END), 0)::BIGINT AS monthly_outflow
FROM workspaces w
JOIN workspace_members wm ON wm.workspace_id = w.id
LEFT JOIN cash_transactions ct ON ct.workspace_id = w.id AND ct.deleted_at IS NULL
WHERE wm.user_id = $1
GROUP BY w.id, wm.role
ORDER BY w.is_default DESC, w.created_at ASC;

-- name: CreateWorkspace :one
INSERT INTO workspaces (
    id, owner_id, name, short_name, focus_type, is_default
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;

-- name: AddWorkspaceMember :exec
INSERT INTO workspace_members (
    workspace_id, user_id, role
) VALUES (
    $1, $2, $3
);

-- name: CreateAuthSession :one
INSERT INTO auth_sessions (
    id, user_id, refresh_token_hash, device_name, ip_address, expires_at
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;

-- name: GetAuthSessionByHash :one
SELECT * FROM auth_sessions
WHERE refresh_token_hash = $1
LIMIT 1;

-- name: RevokeAuthSessionByID :exec
UPDATE auth_sessions
SET revoked_at = NOW()
WHERE id = $1 AND revoked_at IS NULL;

-- name: RevokeAllUserSessions :exec
UPDATE auth_sessions
SET revoked_at = NOW()
WHERE user_id = $1 AND revoked_at IS NULL;

-- name: InsertAuditLog :exec
INSERT INTO audit_logs (
    id, user_id, workspace_id, action, ip_address, metadata_json
) VALUES (
    $1, $2, $3, $4, $5, $6
);
