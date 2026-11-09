-- +goose Up
ALTER TABLE users
  ADD COLUMN totp_secret_enc text,
  ADD COLUMN totp_pending_enc text,
  ADD COLUMN totp_pending_expires_at timestamptz,
  ADD COLUMN totp_enabled_at timestamptz,
  ADD COLUMN totp_last_used_step bigint;

CREATE TABLE user_recovery_codes (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  code_hash bytea NOT NULL,
  used_at timestamptz,
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX user_recovery_codes_user_idx ON user_recovery_codes(user_id);

CREATE TABLE mfa_challenges (
  id uuid PRIMARY KEY,
  user_id uuid NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash bytea NOT NULL UNIQUE,
  attempts smallint NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
  expires_at timestamptz NOT NULL,
  consumed_at timestamptz,
  ip text,
  user_agent text NOT NULL DEFAULT '',
  created_at timestamptz NOT NULL DEFAULT now()
);
CREATE INDEX mfa_challenges_user_idx ON mfa_challenges(user_id);
CREATE INDEX mfa_challenges_expires_idx ON mfa_challenges(expires_at);

-- +goose Down
DROP TABLE mfa_challenges;
DROP TABLE user_recovery_codes;
ALTER TABLE users
  DROP COLUMN totp_last_used_step,
  DROP COLUMN totp_enabled_at,
  DROP COLUMN totp_pending_expires_at,
  DROP COLUMN totp_pending_enc,
  DROP COLUMN totp_secret_enc;
