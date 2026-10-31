-- min_reader_schema: 1
--
-- 0018 adds the learnings of the retro (factorio-1xq0). `loop retro route`
-- writes one row for each learning a retro run sends, with the panel that
-- judged it. The status decides the route: held and counted hold, landed
-- lands, escalated and answered escalate. So no column stores the route,
-- and the reason exists only on the escalate route. It only adds a table,
-- so a schema-17 reader still reads and writes every table it knows.
CREATE TABLE learnings (
    id             INTEGER PRIMARY KEY,
    project        TEXT    NOT NULL,
    retro_item     TEXT    NOT NULL,
    statement      TEXT    NOT NULL,
    cases          TEXT    NOT NULL CHECK (json_valid(cases) AND json_array_length(cases) >= 1),
    extends        INTEGER REFERENCES learnings (id),
    panel          TEXT    NOT NULL CHECK (json_valid(panel)),
    reason         TEXT    NOT NULL DEFAULT '',
    target         TEXT    NOT NULL DEFAULT '',
    status         TEXT    NOT NULL CHECK (status IN ('held', 'landed', 'escalated', 'answered', 'counted')),
    landed_ref     TEXT    NOT NULL DEFAULT '',
    answer         TEXT    NOT NULL DEFAULT '',
    compared_at    INTEGER,
    compared_fault TEXT    NOT NULL DEFAULT '',
    created_at     INTEGER NOT NULL,
    CHECK ((reason <> '') = (status IN ('escalated', 'answered'))),
    CHECK (landed_ref = '' OR status = 'landed')
);
CREATE INDEX learnings_project_status ON learnings (project, status);
CREATE INDEX learnings_retro_item ON learnings (project, retro_item, status);
