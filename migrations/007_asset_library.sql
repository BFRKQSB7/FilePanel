ALTER TABLE shares ADD COLUMN gallery_assets TEXT NOT NULL DEFAULT '[]';
ALTER TABLE assets ADD COLUMN name TEXT NOT NULL DEFAULT '';
-- Preserve the previous implicit screenshot gallery for existing shares.
UPDATE shares SET gallery_assets = COALESCE((
    SELECT json_group_array(id) FROM (
        SELECT id FROM assets
        WHERE assets.file_id=shares.file_id AND assets.id<>shares.cover_asset_id
        ORDER BY created_at,id
    )
), '[]');
