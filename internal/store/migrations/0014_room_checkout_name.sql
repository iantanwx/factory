-- min_reader_schema: 1
--
-- 0014 adds the checkout name of a room (factorio-pwpi). The server names
-- the checkout once, when the room opens, after its project, kind, item and
-- purpose. Each reader uses the stored name, so the cleanup pass keeps the
-- checkout of a room that is still opening. A room opened before 0014 has
-- the name loop-<room id>, and the backfill records it. It only adds a
-- column with a default, so a schema-13 reader still reads and writes every
-- table it knows.
ALTER TABLE rooms ADD COLUMN checkout_name TEXT NOT NULL DEFAULT '';
UPDATE rooms SET checkout_name = 'loop-' || id;
