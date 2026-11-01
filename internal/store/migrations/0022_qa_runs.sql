-- min_reader_schema: 1
--
-- 0022 records the design skill of each run's brief (factorio-nhb5). The
-- QA report reads it to tell whether the run owes visual QA. A run before
-- 0022 held no design skill that a report reads, so the default '' is
-- true of it. It only adds a column with a default, so a schema-21 reader
-- still reads and writes every table it knows.
ALTER TABLE runs ADD COLUMN design_skill TEXT NOT NULL DEFAULT '';
