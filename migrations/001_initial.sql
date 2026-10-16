CREATE TABLE IF NOT EXISTS schema_migrations (
    version INTEGER PRIMARY KEY,
    applied_at TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE IF NOT EXISTS settings (
    key TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS sessions (
    token_hash BLOB PRIMARY KEY,
    csrf_token TEXT NOT NULL,
    expires_at INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS files (
    id TEXT PRIMARY KEY,
    object_key TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    size INTEGER NOT NULL,
    sha256 TEXT NOT NULL,
    state TEXT NOT NULL CHECK (state IN ('receiving','verified','available','failed','deleting')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS uploads (
    id TEXT PRIMARY KEY,
    object_key TEXT NOT NULL UNIQUE,
    temp_key TEXT NOT NULL UNIQUE,
    display_name TEXT NOT NULL,
    expected_size INTEGER,
    received_size INTEGER NOT NULL DEFAULT 0,
    sha256 TEXT,
    state TEXT NOT NULL CHECK (state IN ('receiving','verified','available','failed')),
    error TEXT NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_files_state_created ON files(state, created_at DESC);
CREATE INDEX IF NOT EXISTS idx_sessions_expiry ON sessions(expires_at);
