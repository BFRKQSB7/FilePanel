ALTER TABLE shares ADD COLUMN page_template TEXT NOT NULL DEFAULT 'standard' CHECK(page_template IN ('standard','compact','showcase'));
ALTER TABLE shares ADD COLUMN relay_mode TEXT NOT NULL DEFAULT 'all' CHECK(relay_mode IN ('all','selected'));
ALTER TABLE shares ADD COLUMN relays TEXT NOT NULL DEFAULT '[]';
ALTER TABLE file_moves ADD COLUMN kind TEXT NOT NULL DEFAULT 'move' CHECK(kind IN ('move','copy','remove','move_existing'));
ALTER TABLE file_moves ADD COLUMN reuse_object TEXT NOT NULL DEFAULT '';
CREATE TABLE file_replicas (
    file_id TEXT NOT NULL REFERENCES files(id) ON DELETE CASCADE,
    node_id TEXT NOT NULL,
    object_id TEXT NOT NULL UNIQUE,
    sha256 TEXT NOT NULL,
    size INTEGER NOT NULL,
    state TEXT NOT NULL DEFAULT 'active' CHECK(state IN ('active','removing')),
    PRIMARY KEY(file_id,node_id)
);
