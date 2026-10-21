-- min_reader_schema: 1
--
-- 0002 adds run tokens (KTD12) and the release each run started under
-- (KTD13). Both changes only add, so a schema-1 reader still reads.

-- A token is stored hashed: sha256 of the token, in hex. The token itself
-- lives only in the checkout's .loop/token file and in the server's memory.
CREATE TABLE run_tokens (
    token_hash TEXT    PRIMARY KEY,
    run_id     TEXT    NOT NULL,
    kind       TEXT    NOT NULL CHECK (kind IN ('run', 'room', 'coordinator')),
    project    TEXT    NOT NULL DEFAULT '',
    item       TEXT    NOT NULL DEFAULT '',
    stage      TEXT    NOT NULL DEFAULT '',
    checkout   TEXT    NOT NULL,
    epoch      INTEGER NOT NULL DEFAULT 0 CHECK (epoch >= 0),
    release    TEXT    NOT NULL DEFAULT '',
    issued_at  INTEGER NOT NULL,
    revoked_at INTEGER
) WITHOUT ROWID;
CREATE INDEX run_tokens_run ON run_tokens (run_id);
CREATE INDEX run_tokens_live ON run_tokens (checkout) WHERE revoked_at IS NULL;

-- The release a run started under. The version handshake lets a run of the
-- previous release finish across an upgrade.
ALTER TABLE runs ADD COLUMN release TEXT NOT NULL DEFAULT '';
