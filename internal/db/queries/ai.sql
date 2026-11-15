-- name: CountMonthlyAIUsageByUser :one
SELECT COUNT(*)::BIGINT AS usage_count
FROM ai_usage_logs
WHERE user_id = $1
  AND created_at >= date_trunc('month', NOW() AT TIME ZONE 'Asia/Jakarta') AT TIME ZONE 'Asia/Jakarta';

-- name: InsertAIUsageLog :one
INSERT INTO ai_usage_logs (
    id, workspace_id, user_id, feature_type, provider, tokens_used
) VALUES (
    $1, $2, $3, $4, $5, $6
)
RETURNING *;
