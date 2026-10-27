-- min_reader_schema: 1
--
-- 0011 adds the stage pause (`loop stage pause`, ADR 0004 D3). One row
-- holds one stage in one project, or in every project when project is
-- empty. The primary key keeps one pause for each scope, so a project pause
-- and a pause of every project are two rows. `loop stage resume` deletes
-- the row of its own scope, and the audit rows stage_paused and
-- stage_resumed keep the history. It only adds a table, so a schema-10
-- reader still reads and writes every table it knows, and admits a paused
-- stage.
CREATE TABLE stage_pauses (
    project   TEXT    NOT NULL DEFAULT '',
    stage     TEXT    NOT NULL,
    reason    TEXT    NOT NULL CHECK (reason <> ''),
    paused_by TEXT    NOT NULL,
    paused_at INTEGER NOT NULL,
    PRIMARY KEY (project, stage)
) WITHOUT ROWID;
