-- min_reader_schema: 1
--
-- 0006 adds indexes that the server's reads use. It only adds indexes, so a
-- schema-5 reader still reads and writes every table it knows.

-- A run's checkout: the run listing joins on it, and a settlement sets
-- its state by run.
CREATE INDEX checkouts_run ON checkouts (run_id);

-- The lease a run owns: the checkout cleanup keeps each checkout whose run
-- still holds one.
CREATE INDEX leases_owner ON leases (owner_run_id) WHERE owner_run_id IS NOT NULL;

-- The open alerts and the unanswered questions, which the inbox lists in
-- id order.
CREATE INDEX alerts_open ON alerts (id) WHERE state = 'open';
CREATE INDEX questions_open ON questions (id) WHERE answered_at IS NULL;

-- A stage's last ended runs, which decide a repeated-outcome alert.
CREATE INDEX runs_stage_ended ON runs (project, stage, ended_at);
