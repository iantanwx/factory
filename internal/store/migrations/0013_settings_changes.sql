-- min_reader_schema: 1
--
-- 0013 adds the settings change (factorio-j4x): one change that
-- `loop settings set` proposes through its own pull request. The partial
-- unique index keeps at most one live change for each project, so a
-- second change waits for the first. It only adds a table, so a
-- schema-12 reader still reads and writes every table it knows.
CREATE TABLE settings_changes (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project    TEXT    NOT NULL,
    setting    TEXT    NOT NULL,
    old_value  TEXT    NOT NULL,
    old_source TEXT    NOT NULL CHECK (old_source IN ('project', 'host', 'default')),
    new_value  TEXT,
    reason     TEXT    NOT NULL,
    actor      TEXT    NOT NULL,
    branch     TEXT    NOT NULL,
    base_sha   TEXT    NOT NULL,
    pr         INTEGER,
    head_sha   TEXT,
    state      TEXT    NOT NULL CHECK (state IN ('proposed', 'open', 'handed', 'merged', 'failed')),
    detail     TEXT    NOT NULL DEFAULT '',
    merge_sha  TEXT,
    created_at INTEGER NOT NULL,
    settled_at INTEGER
);
CREATE UNIQUE INDEX settings_changes_live ON settings_changes (project)
    WHERE state IN ('proposed', 'open', 'handed');
