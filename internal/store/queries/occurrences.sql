-- name: GetOccurrence :one
SELECT * FROM schedule_occurrences WHERE schedule_id=$1 AND id=$2;
-- name: LastOccurrence :one
SELECT * FROM schedule_occurrences WHERE schedule_id=$1 ORDER BY scheduled_at DESC,id DESC LIMIT 1;
-- name: LastOccurrences :many
SELECT DISTINCT ON (schedule_id) * FROM schedule_occurrences
WHERE schedule_id = ANY($1::uuid[]) ORDER BY schedule_id,scheduled_at DESC,id DESC;
-- name: ActiveOccurrence :one
SELECT * FROM schedule_occurrences WHERE schedule_id=$1 AND completed_at IS NULL;
-- name: LatestCompletion :one
SELECT COALESCE(finished_at,completed_at)::timestamptz AS boundary FROM schedule_occurrences
WHERE schedule_id=$1 AND state IN ('accepted','failed') AND completed_at IS NOT NULL
ORDER BY COALESCE(finished_at,completed_at) DESC LIMIT 1;
-- name: OccurrenceUpperBound :one
SELECT COALESCE(max(sequence),0)::bigint FROM schedule_occurrences WHERE schedule_id=$1;
-- name: ListOccurrences :many
SELECT * FROM schedule_occurrences WHERE schedule_id = @schedule_id AND sequence <= @upper_sequence::bigint
AND (NOT @has_position::boolean OR (scheduled_at,id) < (@position_time::timestamptz,@position_id::uuid))
ORDER BY scheduled_at DESC,id DESC LIMIT @page_limit::int;
-- name: DueSchedules :many
SELECT id FROM schedules WHERE status='active' AND deleted_at IS NULL AND next_run_at <= $1
AND NOT EXISTS (SELECT 1 FROM schedule_occurrences o WHERE o.schedule_id=schedules.id AND o.completed_at IS NULL AND (o.state != 'accepted' OR o.sync_error_code IS NOT NULL))
ORDER BY next_run_at,id LIMIT 100;
-- name: WorkOccurrences :many
SELECT * FROM schedule_occurrences WHERE completed_at IS NULL AND (next_attempt_at IS NULL OR next_attempt_at <= $1 OR (state='accepted' AND sync_error_code IS NULL AND EXISTS (SELECT 1 FROM schedules s WHERE s.id=schedule_id AND s.next_run_at <= $1)))
ORDER BY next_attempt_at NULLS FIRST,scheduled_at,id LIMIT 100;
-- name: InsertOccurrence :one
INSERT INTO schedule_occurrences(id,schedule_id,scheduled_at,state,created_at,updated_at,completed_at,request_key,error_code)
VALUES ($1,$2,$3,$4,$5,$5,$6,$7,$8) RETURNING *;
-- name: AdvanceSchedule :exec
UPDATE schedules SET next_run_at=$2 WHERE id=$1;
-- name: PrepareOccurrence :one
UPDATE schedule_occurrences SET state='dispatching',request_path=$2,request_body=$3,request_key=$4,fingerprint=$5,reusable=$6,updated_at=$7
WHERE id=$1 AND state='pending' RETURNING *;
-- name: MarkAttempt :one
UPDATE schedule_occurrences SET attempts=attempts+1,uncertain=true,updated_at=$2 WHERE id=$1 AND state='dispatching' RETURNING *;
-- name: RetryOccurrence :exec
UPDATE schedule_occurrences SET error_code=$2,uncertain=$3,next_attempt_at=$4,updated_at=$5 WHERE id=$1;
-- name: FinishDispatch :exec
UPDATE schedule_occurrences SET state=$2,error_code=$3,completed_at=$4,updated_at=$4,next_attempt_at=NULL WHERE id=$1;
-- name: AcceptOccurrence :exec
UPDATE schedule_occurrences SET state='accepted',session_id=$2,run_id=$3,run_status='accepted',observed_at=$4,updated_at=$4,error_code=NULL,next_attempt_at=$4 WHERE id=$1;
-- name: SaveReusableSession :exec
UPDATE schedules SET reusable_session_id=$2,reusable_fingerprint=$3 WHERE id=$1;
-- name: ClearReusableSession :exec
UPDATE schedules SET reusable_session_id=NULL,reusable_fingerprint=NULL WHERE id=$1;
-- name: ObserveRun :exec
UPDATE schedule_occurrences SET run_status=$2,observed_at=$3,updated_at=$3,execution_started_at=$4,finished_at=$5,run_error_code=$6,sync_error_code=NULL,completed_at=$7,next_attempt_at=$8 WHERE id=$1;
-- name: ObserveFailure :exec
UPDATE schedule_occurrences SET sync_error_code=$2,next_attempt_at=$3,updated_at=$4 WHERE id=$1;
-- name: CancelPending :exec
UPDATE schedule_occurrences SET state='cancelled',error_code='schedule_inactive',completed_at=$2,updated_at=$2,next_attempt_at=NULL WHERE schedule_id=$1 AND state='pending';
