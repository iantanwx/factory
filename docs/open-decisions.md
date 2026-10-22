# Open decisions

Decisions left from the v1 review fixes. Each one needs the operator.

## Keep or change

- **HTTP 500 now counts as a temporary error.** A direct merge that gets a
  500 now reads as unknown and retries, instead of being dequeued.
- **Retry counting is unchanged.** Rate-limit and timeout retries still
  count toward an effect's attempt limit. A failed eligibility post retries
  after an hour.
- **`--body --project=x` stays a missing value**, because `--project=x`
  names a known flag. The alternative matches only exact flag names.

## Review items not fixed yet

- Security: the server runs git in agent checkouts without the
  `core.fsmonitor` and hooks protections the mirror has, so an agent's
  checkout config could run commands as the server.
- Security: nothing checks who owns the `/tmp` socket fallback.
- Restore: escalation and question ids can be reused, a snapshot that is
  too new isn't refused, and a stale pending intent can be marked applied.
- If the server crashes between starting a session and recording it, the
  orphaned harness keeps running.
- The coordinator start retries every 15 s with no backoff.
- An escalation routed to a person after its item already moved on is never
  settled.

## Optional

- Rotate the leaked token: the `orca-serve` journal logs the pairing URL
  with its device token.
- Approve one deliberate test kill of the orca daemon. It would show
  whether orca reuses the same session ids after a daemon death, which
  nobody has measured yet.
