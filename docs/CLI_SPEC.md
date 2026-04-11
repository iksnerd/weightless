# `wl` CLI — Weightless Torrent Tool

A single Go binary that creates and downloads hybrid BitTorrent v1+v2 torrents, with built-in tracker registration and passkey authentication.

## `wl create` — Register data

```
wl create [flags] <path>

Flags:
  --name          Display name for the torrent (default: basename of path).
                  For single-file torrents, info.name in the torrent is always
                  the actual filename — --name affects the registry label,
                  magnet URI dn= param, and output .torrent filename only.
                  For directory torrents, --name sets info.name directly.
  --tracker       Tracker base URL (default: http://localhost:8080)
  --piece-length  Piece size in bytes, power of 2, >= 16384 (default: 262144)
  --api-key       API key for authenticated registration
  --user-id       User ID for passkey auth (or set WL_USER_ID env)
  --secret        Tracker secret for passkey signing (or set TRACKER_SECRET env)
  --private       Disable DHT/PEX (make tracker the sole authority)
  --description   Long description of the content
  --publisher     Publisher or organization name
  --license       License identifier (e.g. MIT, Apache-2.0)
  --category      Registry category (e.g. models, datasets)
  --tags          Comma-separated tags
```

## Examples

```bash
# Create torrent from a single file
wl create --name "Llama-Weights-v4" ./llama-v4.safetensors

# Create torrent with full metadata
wl create --name "ImageNet" \
  --description "ImageNet validation set" \
  --publisher "Stanford" \
  --license "Custom" \
  --category "datasets" \
  --tags "vision,benchmark" \
  ./imagenet-val/
```

## What it Does

1. Walks the file/directory and hashes all content
2. Builds a **hybrid v1+v2 torrent**:
   - **v1**: SHA-1 piece hashes (for Transmission, qBittorrent compatibility)
   - **v2**: 16KiB-block SHA-256 Merkle trees with piece layers (BEP 52)
3. Injects the configured `source` tag into the info dict (brands the hash)
4. Writes `<name>.torrent` to the current directory
5. Registers the hashes and metadata with the tracker's `/api/registry` endpoint (JSON POST)
6. Outputs the info hash and magnet link

## Implementation

- **Entry point**: `cmd/wl/main.go`
- **Torrent library**: `internal/torrent/torrent.go` — pure Go, zero-CGO, uses `crypto/sha1`, `crypto/sha256`, and `github.com/zeebo/bencode`
- **No external torrent dependencies** — does not use `anacrolix/torrent` or any CGO libraries

---

## `wl get` — Download from the swarm

```
wl get [flags] <magnet-link>

Flags:
  --tracker       Tracker base URL (default: http://localhost:8080, or WL_TRACKER env)
  --output        Output directory (default: current directory)
  --user-id       User ID for passkey auth (or set WL_USER_ID env)
  --secret        Tracker secret for passkey signing (or set TRACKER_SECRET env)
```

### Examples

```bash
# Download using a magnet link
wl get "magnet:?xt=urn:btih:abc123...&dn=My-Dataset&tr=http://tracker:8080/announce"

# Download to a specific directory
wl get --output ./downloads "magnet:?xt=urn:btih:abc123..."

# With passkey auth
wl get --user-id alice "magnet:?xt=urn:btih:abc123..."
```

### What happens when you run `wl get`

1. Parses the magnet URI — extracts v1/v2 info hashes, tracker URL, display name
2. **Fast path**: Fetches `.torrent` metadata from the tracker's `/api/registry/torrent` API
3. Decodes the bencoded info dict — file list, piece hashes, piece length
4. Saves the `.torrent` file to the output directory
5. Announces to the tracker to discover peers
6. Downloads pieces concurrently from the swarm (multi-worker, configurable)
7. Verifies each piece with SHA-1 hash checking
8. Writes verified data to disk (handles multi-file torrents with correct offsets)

### P2P Features

- **BEP 3** — Peer Wire Protocol (handshake, choke/unchoke, request/piece)
- **BEP 10** — Extension Protocol (negotiates extensions with peers)
- **BEP 9** — Metadata Exchange (fetch info dict from peers when tracker API is unavailable)
- **Concurrent swarm** — Multiple workers download from different peers simultaneously
- **Reconnect on failure** — Automatically retries failed pieces with other peers

---

## Torrent Format (Hybrid v1+v2)

The info dict includes both v1 and v2 fields:

```
info = {
    "file tree": { ... },        # v2: nested path → {length, pieces root}
    "length": N,                  # v1: total file size (single file)
    "meta version": 2,            # v2 marker
    "name": "...",                # shared
    "piece length": 262144,       # shared
    "pieces": <SHA-1 hashes>,     # v1: concatenated 20-byte SHA-1 per piece
    "source": "your-source-tag"     # branding
}
```

For multi-file torrents, `length` is replaced by `files` (v1 file list).

The top-level metainfo also includes `piece layers` (v2 Merkle tree intermediate hashes) for files spanning multiple pieces.

