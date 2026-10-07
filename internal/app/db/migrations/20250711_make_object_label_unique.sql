-- +goose Up
-- Enforce label uniqueness (case-insensitive) for active, labeled rows.
-- Replaces the non-unique partial index created in 20250710 so that a label
-- resolves to exactly one query_object_relationship_map row.
DROP INDEX IF EXISTS idx_qorm_object_label;

CREATE UNIQUE INDEX IF NOT EXISTS idx_qorm_object_label_unique
ON query_object_relationship_map(LOWER(object_label))
WHERE object_label IS NOT NULL AND object_label != '' AND is_deleted = false;

-- +goose Down
DROP INDEX IF EXISTS idx_qorm_object_label_unique;

CREATE INDEX IF NOT EXISTS idx_qorm_object_label
ON query_object_relationship_map(LOWER(object_label))
WHERE object_label IS NOT NULL AND object_label != '' AND is_deleted = false;
