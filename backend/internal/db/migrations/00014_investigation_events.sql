-- +goose Up
-- Investigations add one timeline event kind. The lifecycle events keep their
-- original constraint, so only the allowed list is replaced.
ALTER TABLE incident_events
  DROP CONSTRAINT IF EXISTS incident_events_kind_check;
ALTER TABLE incident_events
  ADD CONSTRAINT incident_events_kind_check
  CHECK (kind IN ('opened', 'acknowledged', 'resolved', 'investigation_completed'));

-- +goose Down
DELETE FROM incident_events WHERE kind = 'investigation_completed';
ALTER TABLE incident_events
  DROP CONSTRAINT IF EXISTS incident_events_kind_check;
ALTER TABLE incident_events
  ADD CONSTRAINT incident_events_kind_check
  CHECK (kind IN ('opened', 'acknowledged', 'resolved'));
