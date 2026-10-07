-- +goose Up
ALTER TABLE query_object_relationship_map
ADD COLUMN IF NOT EXISTS object_label VARCHAR(255) DEFAULT NULL;

CREATE INDEX IF NOT EXISTS idx_qorm_object_label
ON query_object_relationship_map(LOWER(object_label))
WHERE object_label IS NOT NULL AND object_label != '' AND is_deleted = false;

-- +goose Down
DROP INDEX IF EXISTS idx_qorm_object_label;
ALTER TABLE query_object_relationship_map DROP COLUMN IF EXISTS object_label;
