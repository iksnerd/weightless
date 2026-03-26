# TODO

## CLI

- [ ] `wl get <magnet-link>` — minimal downloader command (complement to `wl create`)
- [ ] Multi-tracker support — add `announce-list` with fallback trackers to `.torrent` files
- [ ] Configurable piece length auto-scaling based on file size (small files -> 16KiB, large files -> 4MB+)

## Tracker

- [ ] CORS headers — allow Next.js frontend to call tracker API directly (alternative to proxying through API routes)
- [x] Rate limiting on `/announce` to prevent abuse
- [x] High-performance In-Memory peer tracking (flush to SQLite)
- [x] Registry-Only tracking (reject unregistered hashes to prevent spam)
- [x] Expose Prometheus metrics (`/metrics`)
- [ ] `GET /api/registry/stats` — aggregate stats endpoint (total torrents, total completions, total peers)
- [ ] Verified badge API — endpoint to set `verified=true` for a hash (admin-only, requires API key)

## Registry / Metadata

- [ ] Hugging Face hash verification — auto-check if a registered hash matches official HF model hashes, award "Verified" badge
- [ ] Provenance tracking — store who registered a hash (publisher signature or API key identity)

## Next.js Frontend (Hub)

- [ ] Upload flow — server-side torrent creation (call `wl` binary or use WASM)
- [ ] Browser-side hashing (future) — WASM torrent creator, zero-upload registration

## Growth / Distribution

- [ ] Mirror popular Hugging Face models (large GGUFs, SafeTensors) as Weightless torrents
- [ ] Post mirrors to r/LocalLLaMA and Hacker News
- [ ] GitHub Releases for `wl` binary (Mac/Linux/Windows)
- [ ] `brew tap` for macOS install

## Future / Monetization

- [ ] Users table (`pubkey`, `username`, `tier`, `reputation_score`)
- [ ] Seeding-as-a-Service — paid always-on seedboxes for guaranteed availability
- [ ] Priority indexing — instant registration for paying publishers
- [ ] Supporter badges on the Hub
