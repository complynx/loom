-- name: FindUser :one
SELECT id::text FROM users WHERE issuer=$1 AND subject=$2;
-- name: ReadSettings :one
SELECT doc,version FROM settings WHERE singleton;
-- name: ReadBalance :one
SELECT (balance-reserved)::bigint AS available,spent,reserved FROM billing_accounts WHERE owner_id=$1;
-- name: ReadArtifact :one
SELECT id::text,owner_id::text,org_id,mime,sha256,size,created_at,external_disclosure_allowed,source_service_id FROM artifacts WHERE id=$1 AND deleted_at IS NULL;
-- name: ListArtifactIDs :many
SELECT id::text FROM artifacts WHERE deleted_at IS NULL ORDER BY id;
-- name: ReadRunEvents :many
SELECT seq,type,data,created_at FROM run_events WHERE run_id=$1 AND seq>$2 ORDER BY seq LIMIT $3;