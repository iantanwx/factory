-- min_reader_schema: 1
--
-- 0021 adds the QA stage's records (factorio-nhb5). qa_reports holds each
-- report a QA run hands in, one row for each run. qa_check_runs holds
-- GitHub's id of the one loop/qa check run of each head, so each later
-- post to the head updates that check run. IF NOT EXISTS lets it run
-- again over a store whose user_version was set back.
CREATE TABLE IF NOT EXISTS qa_reports (
    run_id          TEXT    PRIMARY KEY,
    project         TEXT    NOT NULL,
    item            TEXT    NOT NULL,
    head            TEXT    NOT NULL,
    conclusion      TEXT    NOT NULL CHECK (conclusion IN ('success', 'failure', 'neutral')),
    report          TEXT    NOT NULL,
    evidence_commit TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL
);
CREATE INDEX IF NOT EXISTS qa_reports_head ON qa_reports (project, item, head);

CREATE TABLE IF NOT EXISTS qa_check_runs (
    project      TEXT    NOT NULL,
    sha          TEXT    NOT NULL,
    check_run_id INTEGER NOT NULL,
    PRIMARY KEY (project, sha)
) WITHOUT ROWID;
