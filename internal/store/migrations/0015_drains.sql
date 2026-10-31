-- min_reader_schema: 1
--
-- 0015 records each drain of the server (factorio-fnrd, F-57). `loop
-- drain` adds the row when the drain begins, and the next start of the
-- server sets ended_at, since a drain lasts until the server restarts. The
-- stalls part reads the rows, so a fire that the server refused while it
-- drained never counts, also after the restart. A NULL ended_at: the drain
-- is on. It only adds a table, so a schema-14 reader still reads and
-- writes every table it knows.
CREATE TABLE drains (
    started_at INTEGER PRIMARY KEY,
    ended_at   INTEGER
);
