# `wl` CLI — Weightless Torrent Creator

A single Go binary that creates hybrid BitTorrent v1+v2 `.torrent` files branded with the configured `source` tag and registers them with the Weightless tracker.

## Usage

```
wl create [flags] <path>

Flags:
  --name          Display name for the torrent (default: basename of path)
  --tracker       Tracker base URL (default: http://localhost:8080)
  --piece-length  Piece size in bytes, power of 2, >= 16384 (default: 262144)
  --api-key       API key for authenticated registration
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

