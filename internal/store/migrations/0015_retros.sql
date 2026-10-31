-- min_reader_schema: 1
--
-- 0015 adds the retro queue (factorio-1xq0). The machine's Terminal hook
-- inserts one pending row when a write lands an epic in done or closes a
-- bug, in the transaction of that write. The key on (project, item) keeps
-- one retro for each item, so a second insert does nothing. The table
-- starts empty: an item that finished before this migration gets no retro.
-- It only adds a table, so a schema-14 reader still reads and writes every
-- table it knows.
CREATE TABLE retros (
    project     TEXT    NOT NULL,
    item        TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK (kind IN ('epic', 'bug')),
    state       TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'running', 'done', 'failed')),
    run_id      TEXT    NOT NULL DEFAULT '',
    attempts    INTEGER NOT NULL DEFAULT 0,
    created_at  INTEGER NOT NULL,
    finished_at INTEGER,
    PRIMARY KEY (project, item)
);
