-- +goose Up
ALTER TABLE schedules DROP COLUMN env_from;

-- +goose Down
ALTER TABLE schedules ADD COLUMN env_from text[] NOT NULL DEFAULT '{}';
