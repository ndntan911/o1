-- +goose Up
ALTER TABLE subscriptions ADD COLUMN trial_started_at timestamptz;

-- +goose Down
ALTER TABLE subscriptions DROP COLUMN trial_started_at;
