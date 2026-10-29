CREATE TABLE IF NOT EXISTS catalog_session_states (
    session_id text PRIMARY KEY,
    version bigint NOT NULL DEFAULT 0,
    state jsonb NOT NULL,
    updated_at timestamptz NOT NULL DEFAULT now()
);
