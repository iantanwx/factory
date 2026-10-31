-- min_reader_schema: 1
--
-- 0020 renames the room kind intake to brainstorm (factorio-vlle). A room
-- that was open as intake reopens as brainstorm. Every other value of each
-- row stays.
UPDATE rooms SET kind = 'brainstorm' WHERE kind = 'intake';
