-- +goose Up
ALTER TABLE browser_sessions ADD COLUMN email text;

-- +goose Down
ALTER TABLE browser_sessions DROP COLUMN email;
