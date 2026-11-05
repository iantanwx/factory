-- min_reader_schema: 1
--
-- 0023 records the project of the coordinator that prepared each question
-- (factorio-1edo). The inbox routes a question by it. A question before
-- 0023, or one the old session prepared, holds '', and routes by the entry
-- it names. It only adds a column with a default, so a schema-22 reader
-- still reads and writes every table it knows.
ALTER TABLE questions ADD COLUMN project TEXT NOT NULL DEFAULT '';
