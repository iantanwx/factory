-- min_reader_schema: 1
--
-- 0019 renames the inbox entry of a fired decision from
-- decision/<project>/<item> to hold/<project>/<item> (factorio-vlle). A
-- question the coordinator prepared on the entry, and an inbox defer of it,
-- name the new id. Every other value of each row stays.
UPDATE questions SET entry = 'hold/' || substr(entry, length('decision/') + 1)
    WHERE entry LIKE 'decision/%';
UPDATE schedule SET stage = 'hold/' || substr(stage, length('decision/') + 1)
    WHERE kind = 'inbox_defer' AND stage LIKE 'decision/%';
