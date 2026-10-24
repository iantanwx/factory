-- min_reader_schema: 1
--
-- 0010 adds the escalation state closed (F-25). The server closes an
-- escalation that waits for its panel when its item reaches a terminal
-- state, because no panel can route it. SQLite cannot change a CHECK in
-- place, so the table is built again with the same columns, ids and
-- AUTOINCREMENT sequence. A schema-9 reader writes only the states it
-- knows, which the new CHECK allows, and reads a closed escalation as one
-- that waits for no panel.
--
-- Then it closes each escalation that already waits on a terminal item: the
-- item's last applied transition landed in done, canceled, shaped or
-- duplicate, the terminal states of every kind. Their holds go with them.

CREATE TABLE escalations_new (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project    TEXT    NOT NULL,
    item       TEXT    NOT NULL,
    run_id     TEXT    NOT NULL DEFAULT '',
    stage      TEXT    NOT NULL DEFAULT '',
    level      INTEGER NOT NULL CHECK (level BETWEEN 1 AND 5),
    choice     TEXT    NOT NULL CHECK (choice IN ('inside', 'outside')),
    finding    TEXT    NOT NULL,
    state      TEXT    NOT NULL DEFAULT 'awaiting_panel'
                       CHECK (state IN ('awaiting_panel', 'loop', 'person', 'answered', 'closed')),
    panel      TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(panel)),
    routed_by  TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
INSERT INTO escalations_new (id, project, item, run_id, stage, level, choice, finding, state, panel, routed_by, created_at, updated_at)
    SELECT id, project, item, run_id, stage, level, choice, finding, state, panel, routed_by, created_at, updated_at
    FROM escalations;
DELETE FROM sqlite_sequence WHERE name = 'escalations_new';
INSERT INTO sqlite_sequence (name, seq) SELECT 'escalations_new', seq FROM sqlite_sequence WHERE name = 'escalations';
DROP TABLE escalations;
ALTER TABLE escalations_new RENAME TO escalations;
CREATE INDEX escalations_open ON escalations (state, id) WHERE state = 'awaiting_panel';
CREATE INDEX escalations_item ON escalations (project, item, id);

UPDATE escalations SET state = 'closed', updated_at = CAST((julianday('now') - 2440587.5) * 86400000 AS INTEGER)
WHERE state = 'awaiting_panel'
  AND (SELECT t.to_status FROM transitions t WHERE t.project = escalations.project AND t.item = escalations.item
       ORDER BY t.seq DESC LIMIT 1) IN ('done', 'canceled', 'shaped', 'duplicate');
DELETE FROM holds WHERE kind = 'escalation'
  AND holder IN (SELECT 'escalation:' || id FROM escalations WHERE state = 'closed');
