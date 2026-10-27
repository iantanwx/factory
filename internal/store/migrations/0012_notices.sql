-- min_reader_schema: 1
--
-- 0012 adds the notice (factorio-j4x): a change the operator reads, such
-- as a settings change that merged. Nothing waits on it. `loop inbox
-- answer` sets dismissed_at, and the entry leaves the inbox. The unique
-- index on (source, ref) keeps one notice for each change, so a repeated
-- landing tick puts no second one. It only adds a table, so a schema-11
-- reader still reads and writes every table it knows.
CREATE TABLE notices (
    id           INTEGER PRIMARY KEY AUTOINCREMENT,
    project      TEXT    NOT NULL DEFAULT '',
    source       TEXT    NOT NULL,
    ref          TEXT    NOT NULL,
    subject      TEXT    NOT NULL,
    body         TEXT    NOT NULL,
    at           INTEGER NOT NULL,
    dismissed_at INTEGER
);
CREATE UNIQUE INDEX notices_ref ON notices (source, ref);
