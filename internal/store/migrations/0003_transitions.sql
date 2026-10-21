-- min_reader_schema: 1
--
-- 0003 adds the history of applied transitions (U5). Step 3 of KTD6 writes
-- one row in the transaction that marks an intent applied, so an intent
-- has at most one row. It only adds a table, so a schema-2 reader still
-- reads and writes.

CREATE TABLE transitions (
    intent_id   INTEGER PRIMARY KEY REFERENCES intents (id),
    project     TEXT    NOT NULL,
    item        TEXT    NOT NULL,
    seq         INTEGER NOT NULL CHECK (seq > 0),
    kind        TEXT    NOT NULL,
    verb        TEXT    NOT NULL,
    from_status TEXT    NOT NULL,
    to_status   TEXT    NOT NULL,
    epoch       INTEGER NOT NULL CHECK (epoch >= 0),
    actor       TEXT    NOT NULL DEFAULT '',
    run_id      TEXT    NOT NULL DEFAULT '',
    says        TEXT    NOT NULL DEFAULT '',
    facts       TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(facts)),
    at          INTEGER NOT NULL
);
CREATE INDEX transitions_item ON transitions (project, item, seq);
