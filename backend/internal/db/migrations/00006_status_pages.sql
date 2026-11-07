-- +goose Up
CREATE TABLE status_pages (
  workspace_id uuid PRIMARY KEY REFERENCES workspaces(id) ON DELETE CASCADE,
  enabled boolean NOT NULL DEFAULT false,
  title text NOT NULL CHECK (length(trim(title)) BETWEEN 1 AND 80),
  description text NOT NULL DEFAULT '' CHECK (length(description) <= 500),
  created_at timestamptz NOT NULL DEFAULT now(),
  updated_at timestamptz NOT NULL DEFAULT now()
);

CREATE TABLE status_page_monitors (
  status_page_id uuid NOT NULL REFERENCES status_pages(workspace_id) ON DELETE CASCADE,
  monitor_id uuid NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  created_at timestamptz NOT NULL DEFAULT now(),
  PRIMARY KEY (status_page_id, monitor_id)
);

CREATE INDEX status_page_monitors_monitor_idx
  ON status_page_monitors (monitor_id);

-- +goose Down
DROP TABLE status_page_monitors;
DROP TABLE status_pages;
