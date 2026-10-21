-- min_reader_schema: 1
--
-- 0004 adds what the inbox, escalations and rooms keep (U12). It only adds
-- tables and columns with defaults, so a schema-3 reader still reads and
-- writes every table it knows.

-- A hold blocks new admission on one item (KTD23). A room holds its item
-- from open to apply or close. An escalation that waits for the fixer's
-- panel holds its item too, so no stage takes the item before the panel
-- routes it. A running lease is never revoked by a hold.
CREATE TABLE holds (
    project    TEXT    NOT NULL,
    item       TEXT    NOT NULL,
    holder     TEXT    NOT NULL,
    kind       TEXT    NOT NULL CHECK (kind IN ('room', 'escalation')),
    created_at INTEGER NOT NULL,
    PRIMARY KEY (project, item, holder)
) WITHOUT ROWID;

-- One escalation a run raised (KTD22). The fixer's panel routes it: back to
-- the loop with a triaged note, or to a person through the escalated state.
CREATE TABLE escalations (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project    TEXT    NOT NULL,
    item       TEXT    NOT NULL,
    run_id     TEXT    NOT NULL DEFAULT '',
    stage      TEXT    NOT NULL DEFAULT '',
    level      INTEGER NOT NULL CHECK (level BETWEEN 1 AND 5),
    choice     TEXT    NOT NULL CHECK (choice IN ('inside', 'outside')),
    finding    TEXT    NOT NULL,
    state      TEXT    NOT NULL DEFAULT 'awaiting_panel'
                       CHECK (state IN ('awaiting_panel', 'loop', 'person', 'answered')),
    panel      TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(panel)),
    routed_by  TEXT    NOT NULL DEFAULT '',
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX escalations_open ON escalations (state, id) WHERE state = 'awaiting_panel';
CREATE INDEX escalations_item ON escalations (project, item, id);

-- A question the coordinator prepared for the operator (`inbox prepare`).
CREATE TABLE questions (
    id          INTEGER PRIMARY KEY AUTOINCREMENT,
    entry       TEXT    NOT NULL DEFAULT '',
    body        TEXT    NOT NULL,
    run_id      TEXT    NOT NULL DEFAULT '',
    answer      TEXT    NOT NULL DEFAULT '',
    created_at  INTEGER NOT NULL,
    answered_at INTEGER
);

-- A room's kind, its current session and its outcome (KTD23).
ALTER TABLE rooms ADD COLUMN kind TEXT NOT NULL DEFAULT '';
ALTER TABLE rooms ADD COLUMN run_id TEXT NOT NULL DEFAULT '';
ALTER TABLE rooms ADD COLUMN session TEXT NOT NULL DEFAULT '';
ALTER TABLE rooms ADD COLUMN reason TEXT NOT NULL DEFAULT '';
ALTER TABLE rooms ADD COLUMN opened_by TEXT NOT NULL DEFAULT '';
ALTER TABLE rooms ADD COLUMN outcome TEXT NOT NULL DEFAULT '';
ALTER TABLE rooms ADD COLUMN decision TEXT NOT NULL DEFAULT '';
ALTER TABLE rooms ADD COLUMN sessions INTEGER NOT NULL DEFAULT 0;
ALTER TABLE rooms ADD COLUMN updated_at INTEGER NOT NULL DEFAULT 0;
CREATE INDEX rooms_open ON rooms (project, item) WHERE closed_at IS NULL;
