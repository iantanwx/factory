-- min_reader_schema: 1
--
-- 0005 adds loop faults (U18, KTD31) and the fault a run finished on. It
-- only adds a table and a column with a default, so a schema-4 reader
-- still reads and writes every table it knows.

-- One fault in the loop itself, for the whole instance. A fault is never a
-- work item and never enters beads. Its id is F-<n>, where n is the
-- AUTOINCREMENT key, so the server never gives an id out twice, even after
-- a row is deleted. The reporter is the principal that called
-- `fault report`. The release is the loop release the server ran at the
-- report.
CREATE TABLE faults (
    n               INTEGER PRIMARY KEY AUTOINCREMENT,
    text            TEXT    NOT NULL,
    reporter_kind   TEXT    NOT NULL CHECK (reporter_kind IN ('run', 'room', 'coordinator')),
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
CREATE INDEX faults_scope ON faults (project, stage, status);

-- The fault a run named with `run finish --fault`. Empty for every other
-- run.
ALTER TABLE runs ADD COLUMN fault_id TEXT NOT NULL DEFAULT '';
