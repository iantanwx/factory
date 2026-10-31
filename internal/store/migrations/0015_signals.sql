-- min_reader_schema: 1
--
-- 0015 adds the signals of the retro (factorio-1xq0). The server writes a
-- row when an item shows a fault for the second time: a red build, a
-- failed attempt at the same inputs, or a document reject. A later case
-- joins the row of its (project, item, kind, scope). It only adds a table,
-- so a schema-14 reader still reads and writes every table it knows.
CREATE TABLE signals (
    id      INTEGER PRIMARY KEY,
    project TEXT    NOT NULL,
    item    TEXT    NOT NULL,
    kind    TEXT    NOT NULL CHECK (kind IN ('red_build', 'attempts', 'second_reject')),
    scope   TEXT    NOT NULL DEFAULT '',
    cases   TEXT    NOT NULL CHECK (json_valid(cases) AND json_array_length(cases) >= 2),
    at      INTEGER NOT NULL,
    UNIQUE (project, item, kind, scope)
);
