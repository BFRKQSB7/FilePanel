CREATE TABLE IF NOT EXISTS file_moves (
    id TEXT PRIMARY KEY,
    request_id TEXT NOT NULL UNIQUE,
    file_id TEXT NOT NULL,
    source TEXT NOT NULL,
    source_object TEXT NOT NULL,
    target TEXT NOT NULL,
    target_object TEXT NOT NULL UNIQUE,
    sha256 TEXT NOT NULL,
    size INTEGER NOT NULL,
    name TEXT NOT NULL,
    state TEXT NOT NULL DEFAULT 'preflight',
    fence INTEGER NOT NULL DEFAULT 0,
    progress INTEGER NOT NULL DEFAULT 0,
    route TEXT NOT NULL DEFAULT '',
    error TEXT NOT NULL DEFAULT '',
    cancel INTEGER NOT NULL DEFAULT 0,
    updated INTEGER NOT NULL
);
CREATE UNIQUE INDEX IF NOT EXISTS idx_move_active ON file_moves(file_id) WHERE state NOT IN ('done','cancelled');
CREATE TABLE IF NOT EXISTS file_objects (
    file_id TEXT PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
    object_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS transfer_objects (
    transfer_id TEXT PRIMARY KEY REFERENCES node_transfers(id) ON DELETE CASCADE,
    object_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS move_operations (
    id TEXT PRIMARY KEY,
    move_id TEXT NOT NULL REFERENCES file_moves(id),
    fence INTEGER NOT NULL,
    action TEXT NOT NULL,
    node_id TEXT NOT NULL,
    object_id TEXT NOT NULL
);
CREATE TABLE IF NOT EXISTS object_staging (id TEXT PRIMARY KEY);
CREATE UNIQUE INDEX IF NOT EXISTS idx_move_operation ON move_operations(move_id,fence,action,node_id,object_id);
CREATE TRIGGER IF NOT EXISTS guard_move_delete BEFORE UPDATE OF state ON files
WHEN NEW.state='deleting' AND EXISTS(SELECT 1 FROM file_moves WHERE file_id=NEW.id AND state NOT IN ('done','cancelled'))
BEGIN SELECT RAISE(ABORT,'文件移动尚未完成，请先完成或取消移动'); END;
