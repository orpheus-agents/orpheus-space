-- +goose Up
ALTER TABLE schedules ADD COLUMN services text[] NOT NULL DEFAULT '{}';

-- +goose Down
ALTER TABLE schedules DROP COLUMN services;
