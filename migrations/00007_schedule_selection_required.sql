-- +goose Up
ALTER TABLE schedules
    ALTER COLUMN profile SET NOT NULL,
    ALTER COLUMN template SET NOT NULL,
    ADD CONSTRAINT schedule_profile_nonempty CHECK (length(btrim(profile)) > 0),
    ADD CONSTRAINT schedule_template_nonempty CHECK (length(btrim(template)) > 0);
-- +goose Down
ALTER TABLE schedules
    DROP CONSTRAINT schedule_profile_nonempty,
    DROP CONSTRAINT schedule_template_nonempty,
    ALTER COLUMN profile DROP NOT NULL,
    ALTER COLUMN template DROP NOT NULL;
