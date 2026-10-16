-- 公开发布：分享记录与本机图片素材。file_id 随文件删除级联清理。
CREATE TABLE IF NOT EXISTS shares (
    file_id TEXT PRIMARY KEY REFERENCES files(id) ON DELETE CASCADE,
    slug TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL DEFAULT '',
    version TEXT NOT NULL DEFAULT '',
    summary TEXT NOT NULL DEFAULT '',
    markdown TEXT NOT NULL DEFAULT '',
    button_text TEXT NOT NULL DEFAULT '',
    cover_asset_id TEXT NOT NULL DEFAULT '',
    enabled INTEGER NOT NULL DEFAULT 0 CHECK (enabled IN (0,1)),
    featured INTEGER NOT NULL DEFAULT 0 CHECK (featured IN (0,1)),
    auth_version INTEGER NOT NULL DEFAULT 1,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL,
    CHECK (featured = 0 OR enabled = 1)
);

CREATE INDEX IF NOT EXISTS idx_shares_public ON shares(featured, updated_at DESC);

CREATE TABLE IF NOT EXISTS assets (
    id TEXT PRIMARY KEY,
    file_id TEXT NOT NULL REFERENCES shares(file_id) ON DELETE CASCADE,
    object_key TEXT NOT NULL UNIQUE,
    mime TEXT NOT NULL,
    size INTEGER NOT NULL,
    created_at INTEGER NOT NULL
);

CREATE INDEX IF NOT EXISTS idx_assets_file ON assets(file_id);

-- 周期用量：sent/received 仅保存已确认字节；预留与确认分开保存。
CREATE TABLE IF NOT EXISTS usage_periods (
    start INTEGER PRIMARY KEY,
    end INTEGER NOT NULL,
    sent INTEGER NOT NULL DEFAULT 0,
    received INTEGER NOT NULL DEFAULT 0,
    downloads INTEGER NOT NULL DEFAULT 0,
    created_at INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS usage_categories (
    period_start INTEGER NOT NULL REFERENCES usage_periods(start),
    category TEXT NOT NULL,
    direction TEXT NOT NULL CHECK (direction IN ('sent','received')),
    bytes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (period_start, category, direction)
);

CREATE TABLE IF NOT EXISTS usage_ips (
    period_start INTEGER NOT NULL REFERENCES usage_periods(start),
    ip TEXT NOT NULL,
    bytes INTEGER NOT NULL DEFAULT 0,
    PRIMARY KEY (period_start, ip)
);

-- open：活动响应持有；orphaned：进程崩溃/重启后遗留，未确认部分保守占用，直到对账。
CREATE TABLE IF NOT EXISTS usage_reservations (
    id INTEGER PRIMARY KEY AUTOINCREMENT,
    period_start INTEGER NOT NULL REFERENCES usage_periods(start),
    ip TEXT NOT NULL,
    category TEXT NOT NULL,
    reserved INTEGER NOT NULL,
    flushed INTEGER NOT NULL DEFAULT 0,
    state TEXT NOT NULL CHECK (state IN ('open','orphaned')),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
