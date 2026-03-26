# Weightless API Spec

Base URL: `http://localhost:8080` (local) or your deployed tracker URL.

All JSON endpoints return `Content-Type: application/json`.

## Rate Limiting

The API applies a strict IP-based Token Bucket rate limit to prevent abuse:
- **5 requests per second** per IP address
- **10 request burst capacity**

**Exceeding the limit:**
- **`/announce/`:** Returns `200 OK` with a bencoded dictionary: `{"failure reason": "Rate limit exceeded. Please slow down."}`
- **All other endpoints:** Returns `429 Too Many Requests`.

---

## Authentication (Passkeys)

Weightless uses path-based **Signed Passkeys** for authenticated tracking. This allows the tracker to identify users and track their up/down ratios without a central database.

### The Announce URL
`GET /announce/{passkey}?info_hash={bin_hash}&...`

- **passkey**: `[user_id].[signature]`
- **Unauthenticated access**: If `TRACKER_SECRET` is not set on the server, the tracker accepts plain `/announce` (with trailing slash optional).
- **Enforcement**: If `TRACKER_SECRET` is set, requests to `/announce` or invalid passkeys return a bencoded failure reason.

---

## Types

```typescript
interface RegistryEntry {
  info_hash: string;       // hex-encoded SHA-256 v2 hash
  v1_info_hash?: string;   // hex-encoded SHA-1 v1 hash (for hybrid magnet links)
  name: string;            // display name
  verified: boolean;       // verified by admin
  completions: number;     // total completed downloads
  description?: string;    // dataset/model description
  publisher?: string;      // creator/organization
  license?: string;        // e.g. "MIT", "Apache-2.0", "CC-BY-4.0"
  size?: number;           // file size in bytes
  category?: string;       // e.g. "models", "datasets", "tools"
  tags?: string;           // comma-separated, e.g. "llm,weights,fp16"
  seeders: number;         // live seeder count from swarm
  leechers: number;        // live leecher count from swarm
}
```

---

## Endpoints

### `GET /api/registry?info_hash={hash}`

Lookup a single registry entry by info hash.

**Query params:**
| Param | Required | Description |
|-------|----------|-------------|
| `info_hash` | yes | The hex info hash |

**Response:**
- `200` — `RegistryEntry` JSON object
- `400` — Missing info_hash
- `404` — Hash not found

```bash
curl "http://localhost:8080/api/registry?info_hash=bf1a33cb..."
```

```json
{
  "info_hash": "bf1a33cbc65f8e9d018d0666378696f6653d425d727e83ca56e3246083c40615",
  "name": "archive.zip",
  "verified": false,
  "completions": 3,
  "description": "Climate dataset 2026",
  "publisher": "weightless-lab",
  "license": "CC-BY-4.0",
  "size": 236936515,
  "category": "datasets",
  "tags": "climate,2026"
}
```

---

### `GET /api/registry/search`

Search and browse the registry with pagination and sorting.

**Query params (all optional, combine freely):**
| Param | Default | Description |
|-------|---------|-------------|
| `q` | — | Name substring search (case-insensitive LIKE) |
| `category` | — | Exact category match |
| `publisher` | — | Exact publisher match |
| `tags` | — | Tag substring match (searches comma-separated tags) |
| `sort` | `created_at` | Sort order: `created_at`, `completions`, or `seeders` |
| `limit` | `50` | Results per page (max 100) |
| `offset` | `0` | Pagination offset |

**Response headers:**
| Header | Description |
|--------|-------------|
| `X-Total-Count` | Total matching entries (for pagination UI) |

**Response:**
- `200` — `RegistryEntry[]` JSON array (empty `[]` if no matches)

```bash
# Search by name
curl "http://localhost:8080/api/registry/search?q=llama"

# Filter by category, sort by popularity
curl "http://localhost:8080/api/registry/search?category=models&sort=completions"

# Paginate
curl "http://localhost:8080/api/registry/search?limit=20&offset=40"

# Combined
curl "http://localhost:8080/api/registry/search?q=weights&category=models&sort=seeders&limit=10"
```

```json
[
  {
    "info_hash": "bf1a33cb...",
    "name": "Llama Weights v4",
    "verified": true,
    "completions": 42,
    "category": "models",
    "publisher": "meta",
    "seeders": 5,
    "leechers": 2
  },
  {
    "info_hash": "9d0561ef...",
    "name": "Llama Tokenizer",
    "verified": false,
    "completions": 7,
    "seeders": 1,
    "leechers": 0
  }
]
```

---

### `POST /api/registry`

Register or update a torrent entry. Upserts on `info_hash` conflict (updates all fields except `verified` and `completions`).

**Headers:**
| Header | Required | Description |
|--------|----------|-------------|
| `Content-Type` | yes | `application/json` |
| `X-Weightless-Key` | if `REGISTRY_KEY` env is set | API authentication key |

**Body:**
```json
{
  "info_hash": "bf1a33cb...",
  "name": "My Dataset",
  "description": "Optional description",
  "publisher": "my-org",
  "license": "MIT",
  "size": 1048576,
  "category": "datasets",
  "tags": "climate,2026"
}
```

| Field | Required | Type | Description |
|-------|----------|------|-------------|
| `info_hash` | yes | string | Hex info hash |
| `name` | yes | string | Display name |
| `description` | no | string | Description |
| `publisher` | no | string | Creator/org |
| `license` | no | string | License identifier |
| `size` | no | number | File size in bytes |
| `category` | no | string | Category |
| `tags` | no | string | Comma-separated tags |

**Response:**
- `201` — `{"status": "created"}`
- `400` — Missing required fields or invalid JSON
- `401` — Missing/wrong API key (only when `REGISTRY_KEY` is set)

**Max body size:** 1MB

```bash
curl -X POST http://localhost:8080/api/registry \
  -H "Content-Type: application/json" \
  -H "X-Weightless-Key: your-key" \
  -d '{"info_hash":"abc123","name":"Test Dataset","category":"datasets"}'
```

---

### `DELETE /api/registry?info_hash={hash}`

Remove a torrent entry and block the hash from the tracker. Deletes the registry entry, removes all peers, and adds the hash to the blocklist. Blocked hashes are rejected by `/announce` and skipped by `/scrape`.

**Query params:**
| Param | Required | Description |
|-------|----------|-------------|
| `info_hash` | yes | The hex info hash to remove and block |
| `reason` | no | Reason for blocking (stored in blocklist) |

**Headers:**
| Header | Required | Description |
|--------|----------|-------------|
| `X-Weightless-Key` | if `REGISTRY_KEY` env is set | API authentication key |

**Response:**
- `200` — `{"status": "deleted"}`
- `400` — Missing info_hash
- `401` — Missing/wrong API key (only when `REGISTRY_KEY` is set)

```bash
curl -X DELETE "http://localhost:8080/api/registry?info_hash=abc123&reason=dmca" \
  -H "X-Weightless-Key: your-key"
```

---

### `GET /api/registry/torrent?info_hash={hash}`

Download the `.torrent` file associated with a registered hash.

**Query params:**
| Param | Required | Description |
|-------|----------|-------------|
| `info_hash` | yes | The hex info hash |

**Response:**
- `200` — Binary `.torrent` file with `Content-Type: application/x-bittorrent`
- `400` — Missing info_hash
- `404` — Torrent file not found

```bash
curl -L "http://localhost:8080/api/registry/torrent?info_hash=bf1a33cb..." -o myfile.torrent
```

---

### `GET /api/registry/meta?info_hash={hash}`

Returns pre-parsed metadata from the stored `.torrent` file as JSON. Useful for displaying file trees and torrent info in a frontend without client-side bencode parsing.

**Query params:**
| Param | Required | Description |
|-------|----------|-------------|
| `info_hash` | yes | The hex info hash |

**Response:**
- `200` — JSON metadata object
- `400` — Missing info_hash
- `404` — Torrent not found or no torrent data stored

```bash
curl "http://localhost:8080/api/registry/meta?info_hash=bf1a33cb..."
```

```json
{
  "name": "ImageNet-2026",
  "piece_length": 262144,
  "piece_count": 47,
  "total_size": 12345678,
  "files": [
    {"path": "train/data.bin", "length": 10000000},
    {"path": "test/data.bin", "length": 2345678}
  ]
}
```

---

### `GET /health`

Health check for load balancers / readiness probes.

**Response:**
- `200` — `OK` (plain text)
- `503` — `Database unreachable`

---

### `GET /metrics`

Provides real-time tracker operational metrics in Prometheus exposition format. Useful for monitoring load, active swarms, and traffic.

**Response:**
- `200` — Plain text Prometheus metrics

```text
# HELP tracker_announces_total Total number of announce requests handled
tracker_announces_total 1042
# HELP tracker_scrapes_total Total number of scrape requests handled
tracker_scrapes_total 56
# HELP tracker_active_peers Current number of active peers in memory
tracker_active_peers 120
# HELP tracker_registered_torrents Total number of torrents in registry
tracker_registered_torrents 5
# HELP tracker_swarms_total Total number of active swarms
tracker_swarms_total 4
```

---

## Magnet Link Format

For building magnet links in the frontend. Hybrid torrents need **both** v1 and v2 hashes:

```
magnet:?xt=urn:btih:{v1_info_hash}&xt=urn:btmh:1220{info_hash}&dn={name}&tr={tracker_announce_url}
```

- `btih` = v1 SHA-1 hash (what Transmission uses to announce)
- `btmh:1220` = v2 SHA-256 hash (multihash prefix 0x12 = sha2-256, 0x20 = 32 bytes)
- Including both ensures all clients can find peers

**Example:**
```
magnet:?xt=urn:btih:04a86f7b2d7463ab...&xt=urn:btmh:1220bf1a33cbc65f8e9d...&dn=archive.zip&tr=http://localhost:8080/announce
```

### Helper (TypeScript):

```typescript
function buildMagnetLink(entry: RegistryEntry, trackerUrl: string): string {
  const announce = `${trackerUrl}/announce`;
  const params = new URLSearchParams();
  if (entry.v1_info_hash) {
    params.append("xt", `urn:btih:${entry.v1_info_hash}`);
  }
  params.append("xt", `urn:btmh:1220${entry.info_hash}`);
  params.append("dn", entry.name);
  params.append("tr", announce);
  return `magnet:?${params.toString()}`;
}
```

---

## Next.js API Route Examples

### `app/api/torrents/route.ts` — List/Search

```typescript
const TRACKER = process.env.TRACKER_URL || "http://localhost:8080";

export async function GET(req: Request) {
  const { searchParams } = new URL(req.url);
  const params = new URLSearchParams();

  if (searchParams.get("q")) params.set("q", searchParams.get("q")!);
  if (searchParams.get("category")) params.set("category", searchParams.get("category")!);

  const res = await fetch(`${TRACKER}/api/registry/search?${params}`);
  const data = await res.json();
  return Response.json(data);
}
```

### `app/api/torrents/[hash]/route.ts` — Single Entry

```typescript
const TRACKER = process.env.TRACKER_URL || "http://localhost:8080";

export async function GET(_: Request, { params }: { params: { hash: string } }) {
  const res = await fetch(`${TRACKER}/api/registry?info_hash=${params.hash}`);
  if (!res.ok) return new Response("Not found", { status: 404 });
  const data = await res.json();
  return Response.json(data);
}
```

### `app/api/torrents/route.ts` — Register (POST)

```typescript
const TRACKER = process.env.TRACKER_URL || "http://localhost:8080";
const API_KEY = process.env.WEIGHTLESS_API_KEY || "";

export async function POST(req: Request) {
  const body = await req.json();

  const res = await fetch(`${TRACKER}/api/registry`, {
    method: "POST",
    headers: {
      "Content-Type": "application/json",
      ...(API_KEY && { "X-Weightless-Key": API_KEY }),
    },
    body: JSON.stringify(body),
  });

  if (!res.ok) {
    const text = await res.text();
    return new Response(text, { status: res.status });
  }

  return Response.json(await res.json(), { status: 201 });
}
```

---

## CORS

The Go tracker does **not** set CORS headers. For local dev with Next.js, either:
1. Proxy through Next.js API routes (recommended, shown above)
2. Or add a CORS middleware to the tracker

---

## Environment Variables (Next.js `.env.local`)

```env
TRACKER_URL=http://localhost:8080
WEIGHTLESS_API_KEY=your-key-here
```
