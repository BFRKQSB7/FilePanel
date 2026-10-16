-- 站点个性化：全站只有一行配置。revision、素材目录与配置 JSON 在同一条 UPDATE 中切换，
-- 因此重启后要么是旧配置整体，要么是新配置整体。素材文件位于 <data>/profile/sets/<asset_set>/。
CREATE TABLE IF NOT EXISTS site_profile (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    revision INTEGER NOT NULL DEFAULT 0,
    asset_set TEXT NOT NULL DEFAULT '',
    config TEXT NOT NULL DEFAULT '{}',
    updated_at INTEGER NOT NULL DEFAULT 0
);

INSERT OR IGNORE INTO site_profile(id, revision, asset_set, config, updated_at) VALUES (1, 0, '', '{}', 0);
