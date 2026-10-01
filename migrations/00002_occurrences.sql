-- +goose Up
ALTER TABLE schedules ADD COLUMN reusable_session_id uuid;
ALTER TABLE schedules ADD COLUMN reusable_fingerprint text;
CREATE TABLE schedule_occurrences (
 id uuid PRIMARY KEY,
 sequence bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
 schedule_id uuid NOT NULL REFERENCES schedules(id),
 scheduled_at timestamptz NOT NULL,
 state text NOT NULL CHECK (state IN ('pending','dispatching','accepted','skipped','failed','cancelled')),
 created_at timestamptz NOT NULL,
 updated_at timestamptz NOT NULL,
 completed_at timestamptz,
 request_path text,
 request_body bytea,
 request_key uuid NOT NULL,
 fingerprint text,
 reusable boolean NOT NULL DEFAULT false,
 uncertain boolean NOT NULL DEFAULT false,
 attempts integer NOT NULL DEFAULT 0,
 next_attempt_at timestamptz,
 error_code text,
 session_id uuid,
 run_id uuid,
 run_status text,
 observed_at timestamptz,
 execution_started_at timestamptz,
 finished_at timestamptz,
 run_error_code text,
 sync_error_code text,
 UNIQUE(schedule_id,scheduled_at),
 CHECK (state NOT IN ('dispatching','accepted') OR (request_path IS NOT NULL AND request_body IS NOT NULL)),
 CHECK (state != 'accepted' OR (session_id IS NOT NULL AND run_id IS NOT NULL)),
 CHECK (state NOT IN ('skipped','failed','cancelled') OR completed_at IS NOT NULL)
);
CREATE UNIQUE INDEX one_active_occurrence ON schedule_occurrences(schedule_id) WHERE completed_at IS NULL;
CREATE INDEX occurrences_history ON schedule_occurrences(schedule_id,scheduled_at DESC,id DESC);
CREATE INDEX occurrences_pending ON schedule_occurrences(next_attempt_at) WHERE completed_at IS NULL;
-- +goose Down
DROP TABLE schedule_occurrences;
ALTER TABLE schedules DROP COLUMN reusable_fingerprint;
ALTER TABLE schedules DROP COLUMN reusable_session_id;
