-- +goose Up
CREATE TABLE schedules (
    id uuid PRIMARY KEY,
    sequence bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    name text NOT NULL CHECK (length(btrim(name)) BETWEEN 1 AND 200),
    prompt text NOT NULL CHECK (length(btrim(prompt)) > 0),
    cron text NOT NULL,
    timezone text NOT NULL,
    status text NOT NULL CHECK (status IN ('active','paused')),
    model text CHECK (length(btrim(model)) > 0),
    session_mode text NOT NULL CHECK (session_mode IN ('new','reuse')),
    owner_email text CHECK (length(owner_email) > 0 AND owner_email = lower(btrim(owner_email))),
    env_from text[] NOT NULL DEFAULT '{}',
    created_at timestamptz NOT NULL,
    updated_at timestamptz NOT NULL,
    next_run_at timestamptz,
    cron_started_at timestamptz NOT NULL,
    deleted_at timestamptz,
    CHECK ((status = 'paused' OR deleted_at IS NOT NULL) = (next_run_at IS NULL))
);
CREATE INDEX schedules_list ON schedules (created_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX schedules_owner ON schedules (owner_email, created_at DESC, id DESC) WHERE deleted_at IS NULL;
CREATE INDEX schedules_due ON schedules (next_run_at) WHERE status='active' AND deleted_at IS NULL;
CREATE TABLE schedule_create_keys (
    key uuid PRIMARY KEY,
    fingerprint text NOT NULL,
    response jsonb
);
-- +goose Down
DROP TABLE schedule_create_keys;
DROP TABLE schedules;
