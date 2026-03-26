# Stateless Passkey Authentication System

This document details the stateless authentication and usage tracking system implemented in the Weightless tracker.

## 1. Overview

To maintain high performance and avoid a central user database bottleneck, Weightless uses **Cryptographic Signed Passkeys**. This allows the tracker to verify user identity and track data usage without performing a database lookup on every BitTorrent announce.

## 2. Passkey Format

The passkey is a string composed of the User ID and an HMAC-SHA256 signature, separated by a dot:

`[USER_ID].[SIGNATURE]`

- **USER_ID**: The unique identifier from Better Auth (e.g., `user_2N7B...`).
- **SIGNATURE**: `hex(hmac_sha256(USER_ID, TRACKER_SECRET))`

### Example
If `USER_ID` is `user_123` and the secret signature is `a1b2c3...`, the passkey is `user_123.a1b2c3...`.

## 3. The Workflow

### Step A: Generation (Next.js Hub)
When a user visits the Hub to get a Magnet link or `.torrent` file, the Next.js backend:
1. Retrieves the `user_id`.
2. Generates the HMAC signature using the shared `TRACKER_SECRET`.
3. Appends the passkey to the tracker URL:
   `http://localhost:8080/announce/user_123.a1b2c3.../`

### Step B: Verification (Go Tracker)
When the BitTorrent client announces:
1. The tracker extracts the passkey from the URL path.
2. It re-calculates the HMAC of the `user_id`.
3. If the signatures match, the announce is accepted. **Zero database queries required.**

### Step C: Usage Tracking (Tiered Resilience)

Weightless uses a three-tier resilience strategy to ensure zero data loss for seeder usage tracking, even if the Hub is offline for extended periods.

1.  **Tier 1 (High-Speed RAM)**:
    The tracker calculates the bytes uploaded/downloaded since the last announce and stores these **deltas** in a high-speed RAM map: `map[user_id]Usage`.
2.  **Tier 2 (Resilient Flusher)**:
    Every 10 seconds, the tracker attempts to flush the RAM map to the external Hub (`HUB_URL`) via a batch POST request. If the Hub is unreachable or returns an error, the tracker **does not discard the data**.
3.  **Tier 3 (SQLite Failover)**:
    If a Hub sync fails, the usage deltas are immediately moved from RAM into a local SQLite table: `usage_backlog`. This ensures the data survives a tracker restart or container replacement.
4.  **Tier 4 (The Drainer)**:
    A dedicated background worker periodically attempts to "drain" the `usage_backlog` table back to the Hub once it returns online.

## 4. Implementation Details

- **Auth Logic**: `internal/tracker/auth.go`
- **Path Parsing**: `internal/tracker/announce.go`
- **Usage RAM Map**: `internal/tracker/state.go`
- **Background Flusher**: `cmd/tracker/main.go`

## 5. Hub Integration (TypeScript Example)

To generate valid passkeys in your Next.js app:

```typescript
import { createHmac } from 'crypto';

function generatePasskey(userId: string, secret: string): string {
  const signature = createHmac('sha256', secret)
    .update(userId)
    .digest('hex');
  return `${userId}.${signature}`;
}

// Resulting Tracker URL:
// `http://localhost:8080/announce/${generatePasskey(userId, secret)}`
```

## 6. Security Considerations

1. **Secret Rotation**: Changing the `TRACKER_SECRET` instantly revokes all existing passkeys. This is a powerful "kill switch" for the entire network.
2. **Path Encoding**: We put the passkey in the **URL Path** (not query params) to ensure maximum compatibility with BitTorrent clients that might strip or reorder query parameters.
3. **Delta Tracking**: By only syncing *deltas* (changes), the system is resilient to tracker restarts. If the tracker dies, only the last 10 seconds of usage data is lost—a negligible amount in a P2P swarm.
4. **Privacy**: The Better Auth `user_id` is public within the swarm's announce URLs. If absolute privacy is required, the Hub should map `user_id` to a random `internal_id` before signing.
