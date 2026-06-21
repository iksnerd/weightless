# Usage-Sync Contract: Tracker → Hub

**Status:** partially implemented. The tracker side (POST of deltas, header auth, SQLite
backlog on failure) ships today in `internal/tracker/state.go`. The idempotency mechanism
in §4 is **proposed**: it is not yet in the code, and the current behavior has the
double-count hazard described in §4. The Hub endpoint itself lives in the separate Hub
repository.

This is the one cross-component contract in the Weightless system: two independently
deployed processes (the Go tracker and the Next.js Hub) that must agree on a wire format.
Everything else about the Hub is product design, not a contract, and lives in the internal
Hub roadmap note (`HUB_SPEC`).

## 1. Purpose

The tracker attributes upload/download deltas to users via HMAC passkeys but holds no user
database. It periodically POSTs accumulated per-user deltas to the Hub, which owns the
durable user records and increments running totals. The deltas are **incremental** (bytes
since the last successful sync), not absolute totals. That distinction is the whole reason
idempotency matters: replaying an absolute-total message is harmless, replaying a delta
double-counts.

## 2. Endpoint

```
POST {HUB_URL}/api/internal/usage-sync
Content-Type: application/json
X-Weightless-Key: {REGISTRY_KEY}
```

The Hub MUST reject any request whose `X-Weightless-Key` does not match its configured
`REGISTRY_KEY` with `403 Forbidden`. The endpoint is internal and MUST NOT be exposed to
end users.

## 3. Request body

A JSON object mapping user id to a delta object. User ids are the passkey-derived
identifiers the tracker already uses.

```json
{
  "sync_id": "f1c2...e9",
  "deltas": {
    "user_2N7B...": { "Uploaded": 1048576, "Downloaded": 524288 },
    "user_8X2Y...": { "Uploaded": 0, "Downloaded": 209715200 }
  }
}
```

- `Uploaded` and `Downloaded` are non-negative byte counts accumulated since the tracker's
  last successful sync. The Hub MUST treat them as increments to the user's running totals.
- `sync_id` is the idempotency key defined in §4.

> **Migration note.** The shipped tracker currently sends the bare delta map as the
> top-level object (no `sync_id`, no `deltas` wrapper), matching the old HUB_SPEC example.
> Adopting this contract is a coordinated change on both sides: the Hub SHOULD accept the
> legacy bare-map shape (treating it as non-idempotent) until the tracker is upgraded to
> send the wrapped, keyed form.

## 4. Idempotency (the correctness fix)

**The hazard in today's code.** The tracker clears a user's in-RAM delta only after a `200`.
If the Hub commits the increment but the response is lost (a network drop after the Hub's
DB write), the tracker's `client.Do` returns an error, so it keeps the delta and resends it
on the next tick. The Hub then increments a second time. The deltas are silently inflated.
This is a real bug in a flow that is meant to back a billing/ratio mechanic, where
correctness is the point.

**The fix.** Every request carries a unique `sync_id`. The Hub MUST persist processed
`sync_id`s and treat a repeat as a no-op:

1. The tracker generates a fresh `sync_id` per batch and reuses the *same* id on every retry
   of that batch (including a retry served from the SQLite backlog).
2. On receipt, the Hub checks whether `sync_id` was already applied. If so, it MUST return
   `200` without re-incrementing.
3. Only on a fresh `sync_id` does the Hub apply the deltas, recording the id and the
   increment in the same transaction.
4. The tracker clears its in-RAM deltas only after a `200` (idempotent-safe: a re-sent batch
   that the Hub already applied still returns `200`, so the tracker can safely clear).

This makes "did the increment happen?" answerable by the Hub alone, so a lost response no
longer forces a choice between losing data and double-counting.

## 5. Responses and tracker behavior

| Hub response | Meaning | Tracker action |
|---|---|---|
| `200 OK` | applied, or already applied (idempotent) | clear the batch from RAM |
| `403 Forbidden` | bad/missing `X-Weightless-Key` | log; do not retry until key fixed; hold in RAM |
| `4xx` (other) | malformed batch | back up batch to SQLite `usage_backlog`; alert |
| `5xx` | Hub error | back up batch to SQLite `usage_backlog`; retry later with same `sync_id` |
| transport error / timeout | Hub unreachable | hold batch in RAM; retry next tick with same `sync_id` |

The tracker uses a 5-second client timeout. The SQLite `usage_backlog` table
(`user_id, uploaded, downloaded, created_at`) is the durable spillover; a backlog-drain
path resends those rows, and under this contract each drained batch MUST carry a stable
`sync_id` so a drain that races a recovered live sync cannot double-apply.

> **Current vs proposed.** Shipped today: the 5xx/transport rows above, the SQLite backlog,
> and the RAM-hold-on-unreachable behavior. Proposed: the `sync_id` column on every row of
> this table, stable ids across the RAM and backlog paths, and the Hub-side processed-id
> store. Until those land, the `200`-clears-RAM path retains the double-count hazard of §4.

## 6. Open questions

- **Backlog `sync_id` assignment.** Should each `usage_backlog` row carry its original
  batch's `sync_id`, or should drains form a new batch with a new id? The former preserves
  idempotency across a crash; the latter is simpler but reintroduces the hazard.
- **Processed-id retention.** How long must the Hub keep applied `sync_id`s before pruning?
  Long enough to outlive the longest possible tracker retry/backlog window.
- **Per-user vs per-batch granularity.** A batch is all-or-nothing today. If the Hub applies
  some users and fails on others, partial application plus a retry needs per-user idempotency,
  not just per-batch.
</content>
