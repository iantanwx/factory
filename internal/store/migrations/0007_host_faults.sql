-- min_reader_schema: 1
--
-- 0007 lets the operator's own shell report a loop fault: the faults
-- table takes the reporter kind host. SQLite cannot change a CHECK in
-- place, so the table is built again with the same columns. The sequence
-- moves with it, so no fault id is given out twice. A schema-6 reader
-- reads a host row like any other.
CREATE TABLE faults_new (
    n               INTEGER PRIMARY KEY AUTOINCREMENT,
    text            TEXT    NOT NULL,
    reporter_kind   TEXT    NOT NULL CHECK (reporter_kind IN ('run', 'room', 'coordinator', 'host')),
    run_id          TEXT    NOT NULL DEFAULT '',
    project         TEXT    NOT NULL DEFAULT '',
    item            TEXT    NOT NULL DEFAULT '',
    stage           TEXT    NOT NULL DEFAULT '',
    release         TEXT    NOT NULL DEFAULT '',
    status          TEXT    NOT NULL DEFAULT 'open'
        CHECK (status IN ('open', 'acknowledged', 'fixed', 'duplicate', 'not-a-fault')),
    fixed_in        TEXT    NOT NULL DEFAULT '',
    duplicate_of    TEXT    NOT NULL DEFAULT '',
    reason          TEXT    NOT NULL DEFAULT '',
    resolved_by     TEXT    NOT NULL DEFAULT '',
    reported_at     INTEGER NOT NULL,
    acknowledged_at INTEGER,
    resolved_at     INTEGER,
    updated_at      INTEGER NOT NULL
);
INSERT INTO faults_new SELECT * FROM faults;
DELETE FROM sqlite_sequence WHERE name = 'faults_new';
INSERT INTO sqlite_sequence (name, seq) SELECT 'faults_new', seq FROM sqlite_sequence WHERE name = 'faults';
DROP TABLE faults;
ALTER TABLE faults_new RENAME TO faults;
CREATE INDEX faults_scope ON faults (project, stage, status);
