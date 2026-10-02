-- name: CreateSchedule :one
INSERT INTO schedules (id,name,prompt,cron,timezone,status,model,session_mode,owner_email,env_from,created_at,updated_at,next_run_at,cron_started_at,profile,template)
VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$11,$12,$11,$13,$14) RETURNING *;

-- name: GetSchedule :one
SELECT * FROM schedules WHERE id=$1;

-- name: LockSchedule :one
SELECT * FROM schedules WHERE id=$1 FOR UPDATE;

-- name: UpdateSchedule :one
UPDATE schedules SET name=$2,prompt=$3,cron=$4,timezone=$5,status=$6,model=$7,session_mode=$8,owner_email=$9,env_from=$10,updated_at=$11,next_run_at=$12,cron_started_at=$13,profile=$14,template=$15
WHERE id=$1 RETURNING *;

-- name: DeleteSchedule :exec
UPDATE schedules SET deleted_at=$2,updated_at=$2,next_run_at=NULL WHERE id=$1 AND deleted_at IS NULL;

-- name: ListSchedules :many
SELECT * FROM schedules
WHERE deleted_at IS NULL
 AND sequence <= @upper_sequence::bigint
 AND (@status::text = '' OR status = @status::text)
 AND (NOT @unowned::boolean OR owner_email IS NULL)
 AND (cardinality(@owners::text[]) = 0 OR owner_email = ANY(@owners::text[]))
 AND (NOT @has_position::boolean OR (created_at,id) < (@position_time::timestamptz,@position_id::uuid))
ORDER BY created_at DESC,id DESC LIMIT @page_limit::int;

-- name: ScheduleUpperBound :one
SELECT COALESCE(max(sequence),0)::bigint FROM schedules;

-- name: ReserveCreateKey :one
INSERT INTO schedule_create_keys (key,fingerprint) VALUES ($1,$2) ON CONFLICT DO NOTHING RETURNING key;

-- name: GetCreateKey :one
SELECT fingerprint,response FROM schedule_create_keys WHERE key=$1;

-- name: SaveCreateResponse :exec
UPDATE schedule_create_keys SET response=$2 WHERE key=$1;
