-- +goose Up
ALTER TABLE schedules ADD COLUMN profile text, ADD COLUMN template text;
-- +goose Down
ALTER TABLE schedules DROP COLUMN profile, DROP COLUMN template;
