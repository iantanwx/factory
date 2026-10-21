-- min_reader_schema: 1
--
-- 0001 creates every table that the server owns (R11, KTD5). Times are unix
-- milliseconds in INTEGER columns. JSON lives in TEXT columns that a CHECK
-- keeps valid. An item is a beads id, and it is unique only inside its
-- project, so every item key is (project, item).

CREATE TABLE projects (
    id         TEXT    PRIMARY KEY,
    repo       TEXT    NOT NULL,
    config     TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(config)),
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);

-- The last intent sequence for each item (KTD6). The store raises it inside
-- the write transaction that records the intent, so a rolled-back write
-- consumes no sequence number.
CREATE TABLE item_seq (
    project  TEXT    NOT NULL,
    item     TEXT    NOT NULL,
    last_seq INTEGER NOT NULL CHECK (last_seq >= 0),
    PRIMARY KEY (project, item)
) WITHOUT ROWID;

-- Step 1 of KTD6 records an intent. Step 3 settles it.
CREATE TABLE intents (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    project         TEXT    NOT NULL,
    item            TEXT    NOT NULL,
    seq             INTEGER NOT NULL CHECK (seq > 0),
    kind            TEXT    NOT NULL,
    verb            TEXT    NOT NULL,
    idem_key        TEXT,
    expected_status TEXT    NOT NULL DEFAULT '',
    target_status   TEXT    NOT NULL DEFAULT '',
    facts           TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(facts)),
    epoch           INTEGER NOT NULL CHECK (epoch >= 0),
    state           TEXT    NOT NULL DEFAULT 'pending'
                            CHECK (state IN ('pending', 'applied', 'refused')),
    result          TEXT    CHECK (result IS NULL OR json_valid(result)),
    run_id          TEXT    NOT NULL DEFAULT '',
    actor           TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    settled_at      INTEGER,
    UNIQUE (project, item, seq)
);
-- KTD6: an item has at most one unapplied intent.
CREATE UNIQUE INDEX intents_one_pending ON intents (project, item) WHERE state = 'pending';
CREATE UNIQUE INDEX intents_idem_key ON intents (idem_key) WHERE idem_key IS NOT NULL;

-- Outbox effects (KTD6). A row stays held until its intent is applied.
CREATE TABLE outbox (
    id              INTEGER PRIMARY KEY AUTOINCREMENT,
    intent_id       INTEGER NOT NULL REFERENCES intents (id),
    effect_kind     TEXT    NOT NULL,
    natural_key     TEXT    NOT NULL,
    payload         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
    state           TEXT    NOT NULL DEFAULT 'held'
                            CHECK (state IN ('held', 'runnable', 'done', 'failed', 'cancelled')),
    attempts        INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
    next_attempt_at INTEGER NOT NULL DEFAULT 0,
    last_error      TEXT    NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);
CREATE UNIQUE INDEX outbox_natural_key ON outbox (effect_kind, natural_key) WHERE state <> 'cancelled';
CREATE INDEX outbox_intent ON outbox (intent_id);
CREATE INDEX outbox_runnable ON outbox (next_attempt_at, id) WHERE state = 'runnable';

-- Idempotency keys (KTD13). result is NULL while the key's intent is in
-- flight. expires_at is NULL until the key's run settles.
CREATE TABLE idempotency (
    key          TEXT    PRIMARY KEY,
    run_id       TEXT    NOT NULL,
    verb         TEXT    NOT NULL,
    args_hash    TEXT    NOT NULL,
    content_hash TEXT    NOT NULL DEFAULT '',
    intent_id    INTEGER REFERENCES intents (id),
    result       TEXT    CHECK (result IS NULL OR json_valid(result)),
    created_at   INTEGER NOT NULL,
    expires_at   INTEGER
) WITHOUT ROWID;
CREATE INDEX idempotency_run ON idempotency (run_id);
CREATE INDEX idempotency_intent ON idempotency (intent_id) WHERE intent_id IS NOT NULL;
CREATE INDEX idempotency_expiry ON idempotency (expires_at) WHERE expires_at IS NOT NULL;

-- Leases with a fencing epoch (KTD6, KTD7). The row outlives a release, so
-- the epoch never goes back.
CREATE TABLE leases (
    project      TEXT    NOT NULL,
    item         TEXT    NOT NULL,
    epoch        INTEGER NOT NULL CHECK (epoch >= 0),
    owner_run_id TEXT,
    granted_at   INTEGER,
    renewed_at   INTEGER,
    ttl_ms       INTEGER,
    PRIMARY KEY (project, item)
) WITHOUT ROWID;

-- The live-run registry (R11). The agent host stays the truth for liveness.
CREATE TABLE runs (
    run_id           TEXT    PRIMARY KEY,
    project          TEXT    NOT NULL REFERENCES projects (id),
    stage            TEXT    NOT NULL,
    item             TEXT    NOT NULL DEFAULT '',
    harness          TEXT    NOT NULL DEFAULT '',
    model            TEXT    NOT NULL DEFAULT '',
    host_identity    TEXT    NOT NULL DEFAULT '',
    state            TEXT    NOT NULL,
    outcome_class    TEXT,
    pack_hash        TEXT    NOT NULL DEFAULT '',
    started_at       INTEGER NOT NULL,
    ended_at         INTEGER,
    last_cli_call_at INTEGER
);
CREATE INDEX runs_item ON runs (project, item);
CREATE INDEX runs_open ON runs (state) WHERE ended_at IS NULL;

-- A failure is a record keyed by run id, never a counter (KTD6). No foreign
-- key to runs: reconcile can rebuild a failure from a bead fact.
CREATE TABLE failures (
    run_id          TEXT    PRIMARY KEY,
    project         TEXT    NOT NULL,
    item            TEXT    NOT NULL,
    stage           TEXT    NOT NULL,
    inputs_hash     TEXT    NOT NULL,
    release_version TEXT    NOT NULL DEFAULT '',
    class           TEXT    NOT NULL,
    note            TEXT    NOT NULL DEFAULT '',
    recorded_at     INTEGER NOT NULL
) WITHOUT ROWID;
CREATE INDEX failures_inputs ON failures (project, item, stage, inputs_hash);

CREATE TABLE stage_mutexes (
    project       TEXT    NOT NULL,
    stage         TEXT    NOT NULL,
    holder_run_id TEXT    NOT NULL,
    acquired_at   INTEGER NOT NULL,
    PRIMARY KEY (project, stage)
) WITHOUT ROWID;

-- Timed resumes, defers and cron state.
CREATE TABLE schedule (
    id            INTEGER PRIMARY KEY AUTOINCREMENT,
    project       TEXT    NOT NULL,
    kind          TEXT    NOT NULL,
    item          TEXT    NOT NULL DEFAULT '',
    stage         TEXT    NOT NULL DEFAULT '',
    due_at        INTEGER,
    cron_expr     TEXT    NOT NULL DEFAULT '',
    last_fired_at INTEGER,
    payload       TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
    state         TEXT    NOT NULL DEFAULT 'active',
    created_at    INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
);
CREATE INDEX schedule_due ON schedule (due_at) WHERE state = 'active';

CREATE TABLE checkouts (
    id         TEXT    PRIMARY KEY,
    project    TEXT    NOT NULL,
    item       TEXT    NOT NULL DEFAULT '',
    path       TEXT    NOT NULL UNIQUE,
    branch     TEXT    NOT NULL DEFAULT '',
    base_sha   TEXT    NOT NULL DEFAULT '',
    run_id     TEXT,
    state      TEXT    NOT NULL,
    created_at INTEGER NOT NULL,
    updated_at INTEGER NOT NULL
);
CREATE INDEX checkouts_item ON checkouts (project, item);

CREATE TABLE rooms (
    id          TEXT    PRIMARY KEY,
    project     TEXT    NOT NULL,
    item        TEXT    NOT NULL DEFAULT '',
    checkout_id TEXT    REFERENCES checkouts (id),
    state       TEXT    NOT NULL,
    opened_at   INTEGER NOT NULL,
    idle_at     INTEGER,
    closed_at   INTEGER
);

CREATE TABLE alerts (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project    TEXT    NOT NULL DEFAULT '',
    kind       TEXT    NOT NULL,
    subject    TEXT    NOT NULL DEFAULT '',
    dedupe_key TEXT    NOT NULL,
    body       TEXT    NOT NULL DEFAULT '',
    state      TEXT    NOT NULL DEFAULT 'open' CHECK (state IN ('open', 'closed')),
    count      INTEGER NOT NULL DEFAULT 1,
    first_at   INTEGER NOT NULL,
    last_at    INTEGER NOT NULL,
    closed_at  INTEGER
);
CREATE UNIQUE INDEX alerts_open_dedupe ON alerts (dedupe_key) WHERE state = 'open';

CREATE TABLE audit (
    id      INTEGER PRIMARY KEY AUTOINCREMENT,
    at      INTEGER NOT NULL,
    actor   TEXT    NOT NULL,
    project TEXT    NOT NULL DEFAULT '',
    item    TEXT    NOT NULL DEFAULT '',
    action  TEXT    NOT NULL,
    detail  TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(detail))
);
CREATE INDEX audit_item ON audit (project, item, at);

-- Reactor cursors (KTD16). An intent can advance one in its own transaction.
CREATE TABLE github_cursors (
    project    TEXT    NOT NULL,
    stream     TEXT    NOT NULL,
    cursor     TEXT    NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (project, stream)
) WITHOUT ROWID;

CREATE TABLE merge_tracking (
    project     TEXT    NOT NULL,
    pr_number   INTEGER NOT NULL,
    item        TEXT    NOT NULL,
    head_sha    TEXT    NOT NULL DEFAULT '',
    mechanism   TEXT    NOT NULL DEFAULT '',
    state       TEXT    NOT NULL,
    merged_sha  TEXT    NOT NULL DEFAULT '',
    enqueued_at INTEGER,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (project, pr_number)
) WITHOUT ROWID;
CREATE INDEX merge_tracking_item ON merge_tracking (project, item);

-- KTD14: a push first records its intent, so reconcile can tell the server's
-- own remote head from a foreign one.
CREATE TABLE push_intents (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    project    TEXT    NOT NULL,
    item       TEXT    NOT NULL DEFAULT '',
    branch     TEXT    NOT NULL,
    old_sha    TEXT    NOT NULL,
    new_sha    TEXT    NOT NULL,
    run_id     TEXT    NOT NULL DEFAULT '',
    state      TEXT    NOT NULL DEFAULT 'pending' CHECK (state IN ('pending', 'done', 'abandoned')),
    created_at INTEGER NOT NULL,
    settled_at INTEGER
);
CREATE INDEX push_intents_pending ON push_intents (project, branch) WHERE state = 'pending';

CREATE TABLE adapter_checkpoints (
    project    TEXT    NOT NULL,
    adapter    TEXT    NOT NULL,
    checkpoint TEXT    NOT NULL,
    updated_at INTEGER NOT NULL,
    PRIMARY KEY (project, adapter)
) WITHOUT ROWID;

-- KTD8: one row that the server rewrites every 15 seconds, and the outage
-- spans that a start derives from it.
CREATE TABLE liveness_ticks (
    id           INTEGER PRIMARY KEY CHECK (id = 1),
    last_tick_at INTEGER NOT NULL
);

CREATE TABLE outages (
    id         INTEGER PRIMARY KEY AUTOINCREMENT,
    started_at INTEGER NOT NULL,
    ended_at   INTEGER NOT NULL,
    CHECK (ended_at >= started_at)
);

-- KTD17: envelope-encrypted secrets. The key-encryption key never enters
-- this database. key_id and key_version name the wrapping key.
CREATE TABLE secrets (
    project     TEXT    NOT NULL REFERENCES projects (id),
    name        TEXT    NOT NULL,
    key_id      TEXT    NOT NULL,
    key_version INTEGER NOT NULL,
    wrapped_key BLOB    NOT NULL,
    nonce       BLOB    NOT NULL,
    ciphertext  BLOB    NOT NULL,
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (project, name)
) WITHOUT ROWID;
