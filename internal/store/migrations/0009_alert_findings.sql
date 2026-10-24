-- min_reader_schema: 1
--
-- 0009 adds the finding of an open alert (`loop inbox note`): the root
-- cause, the run that wrote it, when, and the alert's count and last_at at
-- that moment. A repeat after the finding counts up the row as before, and
-- the inbox keeps the entry at finding_last_at, so the repeat rings no one.
-- It only adds columns that may be empty, so a schema-8 reader still reads
-- and writes every table it knows.
ALTER TABLE alerts ADD COLUMN finding TEXT NOT NULL DEFAULT '';
ALTER TABLE alerts ADD COLUMN finding_run TEXT NOT NULL DEFAULT '';
ALTER TABLE alerts ADD COLUMN finding_at INTEGER;
ALTER TABLE alerts ADD COLUMN finding_count INTEGER;
ALTER TABLE alerts ADD COLUMN finding_last_at INTEGER;
