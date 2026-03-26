# Weightless

A hybrid BitTorrent v1+v2 (BEP 3/7/48/52) tracker and data registry built in Go. Zero-CGO, single binary, SQLite-backed.

Weightless is a protocol for distributing datasets, model weights, and large files over BitTorrent — with built-in metadata registration, verification, and branding.

## Quick Start

```bash
# Build weightless + CLI
go build -o weightless ./cmd/tracker/
go build -o wl ./cmd/wl/

# Run the tracker
./weightless
# → Weightless Tracker live on :8080

# Create a branded torrent and register it
./wl create --name "My Dataset" --tracker http://localhost:8080 ./path/to/data

# Open the .torrent in Transmission / qBittorrent and start seeding
```

## What it does

- **Registry API** — torrents are registered with metadata (name, publisher, license, category, tags). The tracker only serves peers for registered hashes.
- **Hybrid v1+v2** — creates BEP 52 hybrid torrents and accepts both SHA-1 (20-byte) and SHA-256 (32-byte) info hashes on announce/scrape. v1 and v2 clients share the same registry.
- **In-memory swarm state** — peer data lives in RAM for fast announce responses, with periodic background flush to SQLite.
- **Single binary** — Go + SQLite (WAL mode, zero-CGO). No external databases, no message queues.
- **Scale-to-zero** — runs on Cloud Run or similar. Litestream replicates SQLite to GCS/S3 for durability.
- **Registry-only tracking** — rejects unregistered hashes. No open tracker abuse.

---

## Architecture

```
┌─────────────┐     ┌──────────────┐     ┌─────────────────┐
│  wl CLI     │────▶│  Tracker     │◀────│  BT Clients     │
│  (create)   │     │  (Go binary) │     │  (Transmission)  │
└─────────────┘     └──────┬───────┘     └─────────────────┘
                           │
                    ┌──────▼───────┐
                    │  In-Memory   │ (High Performance,
                    │  Swarm State │  Registry-Only)
                    └──────┬───────┘
                           │ (Periodic Background Flush)
                    ┌──────▼───────┐
                    │  SQLite      │
                    │  (WAL mode)  │
                    └──────────────┘
```

**Key Features of the Tracker:**
- **High-Performance In-Memory State:** Active swarms are tracked in RAM for zero-latency `/announce` responses, with periodic background flushes to SQLite for durability.
- **Registry-Only Tracking:** The tracker is "closed" and will immediately reject any `/announce` requests for `info_hash`es that haven't been explicitly registered via the API. This prevents database bloat and abuse.
- **IP Rate Limiting:** Built-in Token Bucket rate limiter (5 req/sec) protects against DDoS and aggressive scraping.
- **Authenticated Swarms:** Signed passkeys (HMAC-SHA256) for user-level bandwidth tracking without a central user database.
- **Downtime Resilience:** Three-tier usage tracking (RAM -> SQLite backlog -> external sync) handles network outages gracefully.
- **Observability:** Exposes a `/metrics` endpoint in Prometheus format.

**Tracker endpoints:**

| Endpoint | Method | Description |
|----------|--------|-------------|
| `/` | GET | Version string |
| `/announce` | GET | BEP 3/7/52 peer announce (compact format, rate-limited, registry-only) |
| `/scrape` | GET | BEP 48 swarm stats (rate-limited) |
| `/api/registry` | GET | Lookup torrent metadata by info_hash |
| `/api/registry` | POST | Register torrent metadata (JSON body) |
| `/api/registry` | DELETE | Takedown: remove entry + block hash (requires API key) |
| `/api/registry/search` | GET | Search registry by name, category, publisher, tags |
| `/api/registry/torrent` | GET | Download .torrent file by info_hash |
| `/metrics` | GET | Prometheus metrics (swarms, peers, request counts) |
| `/health` | GET | DB health check |

See [docs/API_SPEC.md](docs/API_SPEC.md) for full request/response documentation and Next.js integration examples.

## `wl` CLI — Creating Torrents

The `wl` CLI is how data gets into Weightless. It creates hybrid v1+v2 `.torrent` files branded with the configured `source` tag, registers them with the tracker, and outputs a magnet link.

### Install

**From source (requires Go 1.25+):**
```bash
go build -o wl ./cmd/wl/
sudo mv wl /usr/local/bin/   # optional: make it available system-wide
```

**Cross-compile for distribution:**
```bash
# macOS ARM
GOOS=darwin GOARCH=arm64 go build -o wl-darwin-arm64 ./cmd/wl/

# macOS Intel
GOOS=darwin GOARCH=amd64 go build -o wl-darwin-amd64 ./cmd/wl/

# Linux
GOOS=linux GOARCH=amd64 go build -o wl-linux-amd64 ./cmd/wl/

# Windows
GOOS=windows GOARCH=amd64 go build -o wl-windows-amd64.exe ./cmd/wl/
```

### Usage

```
wl create [flags] <path>

Flags:
  --name          Display name (default: basename of path)
  --tracker       Tracker base URL (default: http://localhost:8080)
  --piece-length  Piece size in bytes, power of 2 (default: 262144)
  --api-key       API key for authenticated registration
  --private       Disable DHT/PEX (make tracker the sole authority)
  --description   Description of the dataset/model
  --publisher     Publisher or organization
  --license       License (e.g. MIT, CC-BY-4.0)
  --category      Category (e.g. models, datasets)
  --tags          Comma-separated tags
```

### Examples

```bash
# Single file
wl create --name "Llama-3-8B.safetensors" ./llama-3-8b.safetensors

# Directory (hashes all files recursively)
wl create --name "ImageNet-2026" ./imagenet/

# With full metadata and API key
wl create --name "Llama-3-Weights" \
  --tracker http://localhost:8080 \
  --api-key your-key-here \
  --description "Llama 3 8B model weights" \
  --publisher "meta" \
  --license "Apache-2.0" \
  --category "models" \
  --tags "llm,llama,8b" \
  ./llama-3-8b.safetensors
```

### What happens when you run `wl create`

1. Reads the file/directory and hashes all content
2. Builds a **hybrid v1+v2 torrent**: SHA-1 pieces (v1 compat) + SHA-256 Merkle trees (v2)
3. Injects the configured `source` tag into the info dict (brands the hash)
4. Writes `<name>.torrent` to the current directory
5. Registers the hash, metadata, file size, and `.torrent` file with the tracker's `/api/registry`
6. Outputs the v1 + v2 info hashes and magnet link (hybrid format, works with all clients)

The `.torrent` file works with any modern client — Transmission 4.x, qBittorrent, Deluge, etc.

## Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `PORT` | `8080` | HTTP listen port |
| `DB_PATH` | `./weightless.db` | SQLite database path |
| `MAX_PEERS` | `50` | Max peers returned per announce |
| `REGISTRY_KEY` | _(unset)_ | If set, POST to `/api/registry` requires `X-Weightless-Key` header |
| `GCS_ACCESS_KEY` | — | Litestream GCS credentials |
| `GCS_SECRET_KEY` | — | Litestream GCS credentials |
| `BACKUP_BUCKET` | — | GCS bucket for Litestream replicas |

## Key Design Decisions

- **Hybrid v1+v2** — Torrents include both SHA-1 pieces (v1) and SHA-256 Merkle trees (v2). Tracker accepts both 20-byte and 32-byte info hashes for broad client compatibility (Transmission, qBittorrent, etc.).
- **Zero-CGO** — Pure Go SQLite via `modernc.org/sqlite`. Single static binary, no system deps.
- **the configured `source` tag branding** — Injected into every torrent's info dict, making hashes unique to the Weightless ecosystem.
- **Public swarm** — No `private` flag. PEX and DHT work alongside the tracker.

## Development

```bash
# Run tests
go test ./...

# Run with coverage
go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out

# Lint
go vet ./...
go fmt ./...

# Clean
go clean -cache && rm -f weightless wl weightless.db*
```

## Deployment

**Local:**
```bash
go run ./cmd/tracker/
```

**App Engine:**
```bash
gcloud app deploy
```

**Docker:**
```bash
docker build -t weightless .
docker run -p 8080:8080 weightless
```

**Cloud Run** (with Litestream replication):
```bash
# See scripts/run.sh and litestream.yml
gcloud run deploy weightless --source .
```

## License

MIT
