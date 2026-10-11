-- Secondary indexes. Dropped while a bulk build is running and recreated by
-- FinishBulk. One statement per line; the index name follows IF NOT EXISTS.
CREATE INDEX IF NOT EXISTS nodes_name       ON nodes(name_id);
CREATE INDEX IF NOT EXISTS nodes_parent     ON nodes(parent_id) WHERE parent_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS nodes_skey       ON nodes(skey) WHERE skey IS NOT NULL;
CREATE INDEX IF NOT EXISTS edges_src        ON edges(src, kind);
CREATE INDEX IF NOT EXISTS edges_unresolved ON edges(name_id) WHERE dst IS NULL OR conf < 80;
CREATE INDEX IF NOT EXISTS files_pkg        ON files(pkg_id) WHERE pkg_id IS NOT NULL;
CREATE INDEX IF NOT EXISTS imports_target   ON imports(target_file) WHERE target_file IS NOT NULL;
