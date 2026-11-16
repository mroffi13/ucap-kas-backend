-- name: CountMonthlyAIUsageByUser :one
SELECT COUNT(*)::BIGINT AS usage_count
FROM ai_usage_logs
WHERE user_id = $1
  AND created_at >= date_trunc('month', NOW() AT TIME ZONE 'Asia/Jakarta') AT TIME ZONE 'Asia/Jakarta';

-- name: GetWorkspaceOwnerUser :one
SELECT u.id, u.google_sub, u.email, u.full_name, u.avatar_url, u.plan_tier, u.plan_expires_at, u.extra_ai_quota, u.created_at, u.updated_at
FROM workspaces w
JOIN users u ON u.id = w.owner_id
WHERE w.id = $1
LIMIT 1;

-- name: CountMonthlyAIUsageByOwner :one
SELECT COUNT(*)::BIGINT AS usage_count
FROM ai_usage_logs l
JOIN workspaces w ON w.id = l.workspace_id
WHERE w.owner_id = $1
  AND l.created_at >= date_trunc('month', NOW() AT TIME ZONE 'Asia/Jakarta') AT TIME ZONE 'Asia/Jakarta';

-- name: CountWeeklyAIUsageByOwner :one
SELECT COUNT(*)::BIGINT AS usage_count
FROM ai_usage_logs l
JOIN workspaces w ON w.id = l.workspace_id
WHERE w.owner_id = $1
  AND l.created_at >= date_trunc('week', NOW() AT TIME ZONE 'Asia/Jakarta') AT TIME ZONE 'Asia/Jakarta';

-- name: InsertAIUsageLog :one
INSERT INTO ai_usage_logs (
    id, workspace_id, user_id, feature_type, provider, tokens_used
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;
