-- min_reader_schema: 1
--
-- 0008 adds what `loop merge status` prints about each PR in the merge
-- pipeline: the reason of its last dequeue, and how many times the
-- hand-off job handed it to a mechanism or re-queued it. It only adds
-- columns with a default, so a schema-7 reader still reads and writes
-- every table it knows.
ALTER TABLE merge_tracking ADD COLUMN reason TEXT NOT NULL DEFAULT '';
ALTER TABLE merge_tracking ADD COLUMN attempts INTEGER NOT NULL DEFAULT 0;
