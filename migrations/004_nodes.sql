CREATE TABLE IF NOT EXISTS nodes (
    id TEXT PRIMARY KEY,
    credential_hash TEXT NOT NULL UNIQUE,
    credential TEXT NOT NULL,
    endpoint TEXT NOT NULL,
    ca TEXT NOT NULL DEFAULT '',
    storage INTEGER NOT NULL DEFAULT 0,
    relay INTEGER NOT NULL DEFAULT 1,
    revoked INTEGER NOT NULL DEFAULT 0,
    version INTEGER NOT NULL DEFAULT 1,
    note TEXT NOT NULL DEFAULT '',
    region TEXT NOT NULL DEFAULT '',
    manual_region INTEGER NOT NULL DEFAULT 0,
    public_ip TEXT NOT NULL DEFAULT '',
    last_seen INTEGER NOT NULL DEFAULT 0,
    last_probe INTEGER NOT NULL DEFAULT 0,
    probe_error TEXT NOT NULL DEFAULT '',
    policy TEXT NOT NULL DEFAULT '{}'
);
CREATE TABLE IF NOT EXISTS node_tickets (
    hash TEXT PRIMARY KEY,
    expires INTEGER NOT NULL,
    used INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS node_locations (
    file_id TEXT PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL REFERENCES nodes(id),
    sha256 TEXT NOT NULL
);
CREATE INDEX IF NOT EXISTS idx_node_locations_node ON node_locations(node_id);
CREATE TABLE IF NOT EXISTS node_transfers (
    id TEXT PRIMARY KEY,
    action TEXT NOT NULL,
    file_id TEXT NOT NULL,
    source TEXT NOT NULL DEFAULT '',
    relay TEXT NOT NULL DEFAULT '',
    sha256 TEXT NOT NULL DEFAULT '',
    name TEXT NOT NULL DEFAULT '',
    visitor_ip TEXT NOT NULL DEFAULT '',
    auth_version INTEGER NOT NULL DEFAULT 0,
    admin_session TEXT NOT NULL DEFAULT '',
    expires INTEGER NOT NULL,
    state TEXT NOT NULL DEFAULT 'pending'
);
CREATE INDEX IF NOT EXISTS idx_node_transfers_expiry ON node_transfers(expires);
CREATE UNIQUE INDEX IF NOT EXISTS idx_node_delete_active ON node_transfers(file_id) WHERE action='delete' AND state='deleting';
CREATE TABLE IF NOT EXISTS node_transfer_leases (
    transfer_id TEXT PRIMARY KEY REFERENCES node_transfers(id) ON DELETE CASCADE,
    until INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS node_reports (
    node_id TEXT NOT NULL REFERENCES nodes(id),
    period INTEGER NOT NULL,
    sequence INTEGER NOT NULL,
    sent INTEGER NOT NULL,
    received INTEGER NOT NULL,
    downloads INTEGER NOT NULL,
    total INTEGER NOT NULL,
    free INTEGER NOT NULL,
    PRIMARY KEY(node_id,period)
);
CREATE TABLE IF NOT EXISTS node_rebinds (
    node_id TEXT PRIMARY KEY REFERENCES nodes(id) ON DELETE CASCADE
);
CREATE TABLE IF NOT EXISTS node_geo_cache (
    ip TEXT PRIMARY KEY,
    region TEXT NOT NULL,
    checked INTEGER NOT NULL
);
CREATE TABLE IF NOT EXISTS usage_days (
    start INTEGER PRIMARY KEY,
    sent INTEGER NOT NULL DEFAULT 0,
    received INTEGER NOT NULL DEFAULT 0,
    downloads INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE IF NOT EXISTS node_days (
    node_id TEXT NOT NULL REFERENCES nodes(id) ON DELETE CASCADE,
    day INTEGER NOT NULL,
    sequence INTEGER NOT NULL,
    sent INTEGER NOT NULL,
    received INTEGER NOT NULL,
    downloads INTEGER NOT NULL,
    PRIMARY KEY(node_id,day)
);
