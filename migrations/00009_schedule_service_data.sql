-- +goose Up
-- Services must be selected explicitly. Preserve schedule timing and historical requests.
UPDATE schedules SET reusable_session_id=NULL,reusable_fingerprint=NULL;
UPDATE schedule_create_keys
SET response=(response - 'env_from') || '{"services":[]}'::jsonb
WHERE response IS NOT NULL;

-- +goose Down
-- Removed ENV selections cannot be reconstructed. Never restore an old reusable context.
UPDATE schedules SET reusable_session_id=NULL,reusable_fingerprint=NULL;
UPDATE schedule_create_keys
SET response=(response - 'services') || '{"env_from":[]}'::jsonb
WHERE response IS NOT NULL;
