-- +goose Up
ALTER TABLE schedule_occurrences
    ADD COLUMN trigger text NOT NULL DEFAULT 'scheduled' CHECK (trigger IN ('scheduled', 'manual')),
    ADD COLUMN manual_key uuid,
    ADD CONSTRAINT occurrence_manual_key CHECK ((trigger = 'manual') = (manual_key IS NOT NULL)),
    DROP CONSTRAINT schedule_occurrences_schedule_id_scheduled_at_key;

CREATE UNIQUE INDEX occurrences_scheduled_time ON schedule_occurrences(schedule_id, scheduled_at)
    WHERE trigger = 'scheduled';
CREATE UNIQUE INDEX occurrences_manual_key ON schedule_occurrences(schedule_id, manual_key)
    WHERE trigger = 'manual';

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM schedule_occurrences WHERE trigger = 'manual') THEN
        RAISE EXCEPTION 'Cannot remove manual occurrence support while manual history exists';
    END IF;
END $$;
-- +goose StatementEnd
DROP INDEX occurrences_manual_key;
DROP INDEX occurrences_scheduled_time;
ALTER TABLE schedule_occurrences
    DROP CONSTRAINT occurrence_manual_key,
    DROP COLUMN manual_key,
    DROP COLUMN trigger,
    ADD CONSTRAINT schedule_occurrences_schedule_id_scheduled_at_key UNIQUE(schedule_id, scheduled_at);
