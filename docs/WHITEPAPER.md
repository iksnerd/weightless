# What BitTorrent Was Missing: A Thin Registry for AI-Scale Data

*Weightless · 2026-06-21*

## Abstract

Weightless puts a verifiable metadata registry and a closed, in-memory tracker in front
of standard hybrid BitTorrent transport, so datasets and model weights become
discoverable and trustworthy without anyone paying a hosting bill for the bytes. The
load-bearing claim is that the *registry*, not the transport, was the missing layer:
BitTorrent solved robust bulk distribution in 2003, and what kept it out of the AI data
stack was the absence of discovery, provenance, and cheap operability as a managed
service. That layer costs close to nothing to run because it stores metadata and peer
liveness, never the content. The system is deployed and interoperates with stock clients
(Transmission 4.x, qBittorrent); large-swarm validation and adoption are still early, and
this paper says so where it matters.

## What it does

A researcher who needs the 800 GB Pile, the 22 GB Wikipedia dump, or a 70 GB model weight
file has two bad options today. Download it over HTTP from a central host, where the
connection drops partway, the partial file corrupts, there is no resume, and the host pays
for every gigabyte of egress. Or find it on Academic Torrents, where the transport is
fine but there is no registry, no metadata API, and no way to verify who published it or
whether it is what they claim.

The hubs (Hugging Face, Kaggle) hide the first problem by spending real money on CDN
egress every month. They do not solve the underlying mismatch: HTTP is the wrong protocol
for moving hundreds of gigabytes to many recipients. BitTorrent is the right one, and has
been for two decades. The reason it never displaced the hubs for this use case is not
transport. It is the three things a hub provides that a swarm does not:

1. **Discovery.** A place to search by name, publisher, license, and tags, and to read
   seeder and leecher counts before you commit to a download.
2. **Provenance.** A way to answer "who published this 70 GB file, and is it the official
   release?" You can read a GitHub repo. You cannot eyeball a weight file.
3. **Operability.** Something a small team can actually run and keep running, without a
   storage budget that scales with popularity.

These are the hard sub-problems. Weightless's argument is that you can deliver all three
on top of unmodified BitTorrent transport, at a marginal cost near zero, and that doing so
is the whole contribution. The bytes still move peer to peer. Only the metadata and the
peer-coordination touch a server, and both are small.

## Technical framework

### Hybrid v1+v2 torrents, so every client works

`wl create` builds a single torrent carrying both BitTorrent v1 (SHA-1 pieces) and v2
(BEP 52 SHA-256 Merkle trees, 16 KiB leaf blocks) in one info dictionary. The v1 info
hash is the SHA-1 of the bencoded info dict; the v2 info hash is the SHA-256 of the same
bytes. The magnet link advertises both (`urn:btih` and `urn:btmh:1220`).

Why hybrid rather than picking one. v2 gives per-file Merkle trees, which means a corrupt
or malicious block is detected at 16 KiB granularity instead of at the piece level, and it
fixes v1's habit of hashing across file boundaries. But most deployed clients still speak
v1. Shipping both in one file means a Transmission user and a v2-aware client join the same
swarm and share the same registry entry, with no fork in the catalog. The cost is a second
set of piece hashes in the metadata, which is negligible against the payload.

### A registry that stores metadata, never bytes

The registry is a JSON API over SQLite. A torrent is registered with its name, publisher,
license, category, tags, and the torrent file itself; the registry can return pre-parsed
metadata (file tree, piece info, total size) and live seeder and leecher counts aggregated
across the v1 and v2 hashes for the same content. This is the discovery layer, and it is
the entire reason a swarm becomes a browsable catalog.

The registry is also what makes the tracker *closed*. The tracker rejects any announce for
an info hash that was never registered. This is a deliberate inversion of the usual open
tracker. It means the database cannot be filled with junk swarms by anyone who points a
client at the port, and it means the operator knows exactly what they are serving. A
takedown is a single DELETE that removes the entry and blocks the hash.

### A tracker built to cost nothing at rest

Active swarm state (which peers hold which torrent, last seen) lives entirely in RAM and is
flushed to SQLite on a 10-second timer. Announce responses never touch disk on the hot
path. Peers come back in BEP 23 compact form, capped at 50 per announce by default. The
process is a single static binary with zero CGO (pure-Go SQLite via `modernc.org/sqlite`),
which means it deploys to Cloud Run and scales to zero when idle. Litestream streams the
SQLite WAL to object storage continuously, so scale-to-zero does not mean data loss.

Every public input boundary is a recognizer, not an ad-hoc parser. The announce handler
validates against a typed grammar before any business logic runs; the bencode decoder is
bounded; the BEP 3 handshake is strict. This is the LangSec discipline: recognize the input
against a formal shape, then act, never the reverse. It matters precisely because the
tracker is a public port that accepts binary input from anonymous clients.

### Accounting without a user database

When a tracker secret is set, the announce URL carries an HMAC-SHA256 passkey derived from
a user id. The tracker can attribute upload and download deltas to a user without holding a
user table itself: the signature is self-verifying, and deltas accumulate in RAM, spill to
a SQLite backlog if the upstream sync target is unreachable, and flush to it when it
returns. This three-tier path (RAM, then SQLite backlog, then external sync) is what keeps
accounting honest across a network outage instead of silently dropping it.

## The core insight: separate the index from the bytes

The one idea worth holding onto is that a hub conflates two jobs that have completely
different cost curves. Indexing content (names, hashes, who-has-what, who-published-it) is
small, bounded, and cheap. Serving content (the actual gigabytes) is large, unbounded, and
expensive, and its cost grows with exactly the thing you want, which is popularity.

Hugging Face and Kaggle run both jobs on their own infrastructure, so their bill scales
with success. Academic Torrents split the bytes out to a swarm but never built the index,
so it is unusable as a catalog. Weightless's design is the clean separation: the swarm does
the expensive, unbounded job it is already good at, and a deliberately thin server does the
cheap, bounded job. The server holds the index and the peer liveness, both of which fit in
memory and flush to a file. Nothing about the design's cost grows with how much data the
network moves. That is why it can run at scale-to-zero on a free tier, and it is the reason
the economics work where a re-hosting approach cannot.

Provenance rides on the same separation. The registry can hold a publisher signature over
the content's identity (its info hashes) without holding the content. A verifier checks the
signature against the hashes it already has from the torrent, so trust is established
without the trusting party ever fetching a byte from the publisher directly. The signing
scheme is specified separately (see [RFC 0001](RFC-0001-torrent-signing.md)) and is not yet
implemented; it is planned work, not a shipped claim.

## Operational experience

Weightless is deployed on Cloud Run with Litestream replication and has been driven
end to end against stock clients: `wl create` produces a hybrid torrent that Transmission
4.x opens and seeds, a second client joins the swarm through the closed tracker, and `wl
get` resolves a magnet, fetches the info dict over BEP 9 when needed, and downloads with
per-piece SHA-1 verification. Peer exchange (BEP 11) and HTTP web-seed fallback (BEP 19)
are in place for swarms that are sparse or briefly seedless.

What is honest to report as constants rather than as adjectives: 16 KiB Merkle blocks,
256 KiB default pieces, a 5 request/second per-IP token bucket with a burst of 10, a
10-second state flush, up to 50 peers returned per announce, one static binary, and a
Prometheus `/metrics` endpoint for swarms, peers, and request counts. The whole tracker is
a few thousand lines of Go with no external services behind it.

What is honest to report as *not yet reached*: there is no public deployment carrying a
thousand-downloader swarm, and the in-memory state model has not been pushed to the point
where flush latency or memory becomes the bottleneck. The design has a clear ceiling (a
single process holding all active swarm state in RAM), and that ceiling has not been
measured because real traffic has not approached it. Naming it is the point; the number
will come from load, not from this paper.

## Limits and boundaries

**Boundaries (out of scope by design).** Weightless does not host content and never will;
if every peer and every web seed for a torrent goes offline, the content is gone, exactly
as with any swarm. It is not a general open tracker; the closed, registry-only model is
deliberate and the `OPEN_TRACKER` escape hatch exists mostly for testing. It does not
implement a DHT node, so discovery is registry-mediated rather than fully trackerless
(a DHT path is on the roadmap, not in the binary).

**Limits (where it has not been pushed).** The single-process in-memory swarm map is the
scaling ceiling, unmeasured under real load. Ratio enforcement, the mechanism a
seeder-economy business model would need, is designed but not built; today the tracker
accounts usage, it does not gate on it. Publisher signing is specified but unimplemented.
These are stated plainly so that the first reader who looks for them finds them here rather
than discovering a gap and discounting the rest.

## References

- Cohen, B. *Incentives Build Robustness in BitTorrent.* 2003. The argument that the
  transport layer is already sound.
- BEP 3 (BitTorrent protocol), BEP 7/23 (compact peers, IPv6), BEP 9 (metadata exchange),
  BEP 11 (PEX), BEP 19 (web seeds), BEP 48 (scrape), BEP 52 (v2). bittorrent.org.
- [docs/API_SPEC.md](API_SPEC.md) for the registry and tracker endpoints.
- [RFC 0001](RFC-0001-torrent-signing.md) for the proposed provenance scheme.
</content>
</invoke>
