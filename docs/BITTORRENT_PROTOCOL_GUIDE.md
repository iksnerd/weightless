# BitTorrent — From Bram Cohen to Weightless

A complete walkthrough of the BitTorrent protocol: the people, the history, the specs, and where Weightless fits. Written so you can talk to anyone in this space with confidence.

---

## The Origin Story (2001–2004)

### Bram Cohen and the Core Insight

In April 2001, **Bram Cohen** — a programmer from New York who'd been coding since age 5 on a Timex Sinclair — quit his job at MojoNation (an early P2P project) and started working on a new protocol. The existing P2P systems (Napster, Gnutella, Kazaa) had a fundamental problem: they relied on central indexes or direct connections that collapsed under load. The more popular a file got, the slower it became to download.

Cohen's insight was the opposite: **the more people who want a file, the faster it should get.** Break files into small pieces. Every downloader simultaneously uploads the pieces they already have. The swarm collectively assembles the file, and the load is distributed across everyone.

He released the first BitTorrent client on **July 2, 2001** and presented the protocol at **CodeCon 2002** in San Francisco — a conference he co-created with his roommate Len Sassaman (a cryptographer who later contributed to PGP and Tor).

By 2004, BitTorrent accounted for an estimated **25–35% of all internet traffic**. Cohen formed **BitTorrent, Inc.** with his brother Ross and business partner Ashwin Navin.

### The Protocol Design (BEP 3)

Cohen also invented **Bencoding** — a simple data serialization format (strings, integers, lists, dictionaries) used for `.torrent` files and tracker communication. It's deliberately minimal: no floats, no nulls, no nested types beyond what's needed.

The core protocol (later formalized as **BEP 3**) works like this:

1. **The `.torrent` file** — metadata containing file names, sizes, piece length, and SHA-1 hashes of each piece. The `info` dictionary is bencoded and SHA-1 hashed to produce the **info-hash** (20 bytes) — the unique ID for the torrent.

2. **The tracker** — an HTTP server. Clients `GET /announce?info_hash=<hash>&peer_id=<id>&port=<port>` to register themselves and get a list of other peers (IP + port).

3. **The peer wire protocol** — TCP connections between peers. Starts with a handshake containing the info-hash (to verify both sides want the same torrent). Then a state machine:
   - **choke/unchoke** — control who you upload to
   - **interested/not-interested** — signal what you want
   - **request/piece** — ask for and receive 16KiB blocks
   - **have/bitfield** — announce which pieces you have

4. **Piece selection** — **rarest-first**: download the pieces that fewest peers have, to maximize availability across the swarm. In **endgame mode** (nearly complete), request remaining pieces from all peers simultaneously.

5. **Tit-for-tat** — upload to the 4 peers who upload fastest to you. One slot rotates randomly ("optimistic unchoking") to discover new fast peers. This incentivizes contributing bandwidth.

**Weightless status:** The tracker implements BEP 3 announce/scrape. `wl create` generates SHA-1 v1 piece hashes. `wl get` (Milestone 1) will implement the peer wire protocol.

---

## The Extension Era (2005–2012)

### Key People

- **Arvid Norberg** (Stockholm, Sweden) — started **libtorrent** in 2004. This C++ library became the engine behind qBittorrent, Deluge, and many others. Norberg remains the principal developer through 2026 and has been critical to every major protocol evolution.
- **Ludvig Strigeus** — created **μTorrent** (2005), a tiny, fast Windows client that popularized BitTorrent for mainstream users. Later acquired by BitTorrent, Inc.
- **the8472** — pseudonymous developer who did most of the specification work for BEP 52 (BitTorrent v2). Major contributor to the protocol's modernization.
- **Steven Siloti** — implemented most of the BEP 52 support in libtorrent.

### DHT — Trackerless Torrents (BEP 5, 2005)

**The problem:** Trackers are single points of failure. If the tracker goes down, no new peers can join.

**The solution:** A **Distributed Hash Table** based on **Kademlia** (an algorithm from 2002). Each node has a 160-bit ID. Nodes are organized by XOR distance — closer IDs mean closer positions in the routing table. Key operations:
- `ping` — are you alive?
- `find_node` — who's closer to this ID?
- `get_peers` — who has this torrent?
- `announce_peer` — I have this torrent

**Two incompatible DHTs emerged:** Azureus (Vuze) shipped first, then "Mainline DHT" appeared in the official BitTorrent client three weeks later. Mainline won — it's now used by μTorrent, Transmission, qBittorrent, Deluge, and most others. Millions of nodes, enabled by default.

**Weightless status:** NOT implemented. The tracker is the sole peer discovery mechanism. DHT is future roadmap (post-grant). This is a deliberate choice — registry-only tracking gives you control over what's tracked and prevents abuse.

### PEX — Peer Exchange (BEP 11, 2005)

**What it is:** Peers gossip about other peers they know. When you're connected to someone, they tell you about other peers in the swarm. Faster than polling the tracker, reduces tracker load.

**Weightless status:** Not implemented in the tracker (it's a peer-to-peer extension, not a tracker feature). Could be part of `wl get` in the future.

### Extension Protocol (BEP 10, 2008)

**The framework for all extensions.** During the handshake, clients set a reserved bit indicating extension support. Then they exchange an `extended handshake` — a dictionary listing which extensions they support (`ut_metadata`, `ut_pex`, etc.) with locally-assigned message IDs.

This is the foundation for BEP 9, PEX, and every modern extension.

**Weightless status:** Part of Milestone 1 (`wl get`). Needed for BEP 9 metadata exchange.

### Metadata Exchange (BEP 9, 2008)

**The problem:** Magnet links contain only the info-hash and tracker URL — no file list, no piece hashes. You need the full `.torrent` metadata to start downloading.

**The solution:** Download the metadata from peers:
1. Connect to peers via tracker or DHT
2. Negotiate `ut_metadata` via BEP 10
3. Request info dictionary in 16KiB chunks
4. Verify: SHA-1(assembled info dict) == info-hash from magnet link
5. Now you have full metadata — proceed to download

**Weightless status:** Part of Milestone 1. Dual-path strategy: registry API as fast path (instant, one HTTP call), BEP 9 as standard path (works with any magnet link).

### Scrape (BEP 48, 2008)

**What it is:** Query the tracker for swarm stats without a full announce. Returns seeders, leechers, and completions per info-hash.

**Weightless status:** Implemented with dual-hash lookup for hybrid v1+v2 support.

### Torrent Signing (BEP 35, 2008)

**The first attempt at provenance.** RSA signatures + X.509 certificates embedded in the `.torrent` file. A signing entity signs the info dictionary; clients verify against a trusted certificate database.

**Why it failed:** RSA/X.509 is too heavy. Requires certificate chains, PKI infrastructure, DER encoding. No major client ever implemented verification. The signing tool from BitTorrent, Inc. is unmaintained. Impractical in browser/WASM contexts.

**This is the gap Weightless fills** — replace RSA/X.509 with Ed25519 (lightweight, no PKI), support hybrid v1+v2, make it verifiable in browsers.

### Magnet Links (2008–2012)

Magnet links (`magnet:?xt=urn:btih:<hash>&tr=<tracker>`) replaced `.torrent` file distribution. Sites like The Pirate Bay switched to magnet-only in 2012. Combined with DHT and BEP 9, this meant you only needed a short text string to start a download — no file hosting needed.

---

## The Corporate Era and Crypto (2012–2020)

### BitTorrent, Inc. — Rise and Sale

BitTorrent, Inc. tried to go legitimate: BitTorrent Sync (later Resilio Sync), BitTorrent Live (streaming). The company struggled to monetize the protocol.

In **June 2018**, **Justin Sun** (founder of the Tron blockchain) acquired BitTorrent, Inc. for **$140 million**. The deal was contentious — Sun filed a temporary restraining order when BitTorrent started talking to other bidders. The company was later renamed **Rainberry Inc.** and integrated with Tron's crypto ecosystem (BTT token, incentivized seeding).

**Bram Cohen** had already left before the acquisition. He went on to found **Chia Network** — a cryptocurrency using "proof of space and time" instead of proof of work. He has no involvement with BitTorrent/Tron.

The **protocol itself** remained open and community-driven. The BEP process continued independently of the company. The real engineering moved to libtorrent (Arvid Norberg) and client developers.

### The SHA-1 Crisis (2017)

In February 2017, Google announced **SHAttered** — the first practical SHA-1 collision. Two different PDF files with the same SHA-1 hash. This meant, in theory, an attacker could craft a malicious file that matches a legitimate torrent's piece hashes.

For BitTorrent, this was an existential threat to integrity verification. The info-hash (SHA-1 of the info dict) and all piece hashes were SHA-1. The protocol needed to move to SHA-256.

---

## The Hash Transition (2017–2026)

### BEP 52 — BitTorrent v2 (2017, Draft — de facto standard)

**Primary author:** the8472, with libtorrent implementation by Steven Siloti.

**Key changes:**
- **SHA-256 info-hash** (32 bytes, replacing 20-byte SHA-1)
- **Per-file Merkle trees** — SHA-256 with 16KiB leaf blocks (instead of flat piece hashes)
- **File alignment** — pieces don't span file boundaries anymore
- **Padding files** — to align files within pieces
- **`file tree`** dictionary — replaces the flat `files` list, supports per-file root hashes

**Hybrid mode:** BEP 52 defines a format containing BOTH v1 and v2 fields. This produces two info-hashes — a 20-byte SHA-1 (for v1 clients) and a 32-byte SHA-256 (for v2 clients).

**The swarm fragmentation problem:** The info-hash is used in the peer wire handshake. v1 clients and v2-only clients use different hashes → they can't handshake → separate swarms. Hybrid clients bridge both sides, but it's a workaround, not a solution.

**Adoption by 2026:**
- **libtorrent 2.0+** (September 2020) — full BEP 52 support → powers qBittorrent, Deluge
- **Transmission 4.0** (2023) — hybrid torrent support
- **BiglyBT** — native v2
- Still listed as "Draft" on bittorrent.org, but is the de facto standard

**Weightless status:** Fully implemented at both creation and tracker level:
- `wl create` builds hybrid v1+v2 torrents (SHA-1 pieces + SHA-256 Merkle trees)
- Tracker accepts both 20-byte and 32-byte info-hashes
- Registry stores `info_hash` (SHA-256) and `v1_info_hash` (SHA-1)
- Dual-lookup: `WHERE info_hash = ? OR v1_info_hash = ?`
- Magnet links include both `btih` and `btmh`
- Torrents branded with `source: your-source-tag`

### DHT Identity — BEP 44 and BEP 46 (2014–2016)

**BEP 44 — Storing Arbitrary Data in the DHT (2014, Accepted)**

Extends the DHT beyond peer discovery. Two storage types:
- **Immutable:** key = SHA-1(data)
- **Mutable:** key = **Ed25519 public key**, data is signed, updates require increasing sequence numbers

This established **Ed25519 as the identity primitive** in BitTorrent. 32-byte public keys, 64-byte signatures. Implemented in libtorrent.

**BEP 46 — Updating Torrents via DHT Mutable Items (2016, Draft)**

The "follow a publisher" primitive. Magnet link format: `magnet:?xs=urn:btpk:<public-key-hex>`. Client looks up the key in DHT, gets a signed info-hash, downloads the torrent. Publisher can update what the link points to.

**Limitations:** v1 only (20-byte hash), no metadata, requires DHT.

**Weightless status:** Not implemented (requires DHT). But the key format decision is made: raw 32-byte Ed25519 keys (BEP 44 compatible). Same key works for registry signing now and DHT identity later.

### BitTorrent v3 — Tixati Specification (October 2025)

**Author:** Kevin Hearn (Tixati). No BEP number.

**The approach:** Instead of replacing the info-hash (like BEP 52), keep the SHA-1 info-hash and add **sidecar hashes**:
- `piece_hashes` field: SHA2-256 or SHA3-256 hashes alongside SHA-1 pieces
- `info_pow` field: proof-of-work on the info dictionary (makes forging expensive)

**Key advantage:** One info-hash → one swarm → no fragmentation. v1 clients ignore the new fields. v3 clients verify both.

**v3.1** adds privacy from DHT scraper bots and aims to fully retire SHA-1.

**Adoption (March 2026):** Tixati 3.39 only. No libtorrent, no qBittorrent, no Transmission.

**Weightless status:** Not implemented. The tracker is already compatible (v3 keeps SHA-1 info-hashes, existing dual-lookup works). Adding v3 torrent creation would be ~3-4 days if the ecosystem adopts it.

**References:**
- v3: https://tixati.com/specs/bittorrent/v3
- v3.1: https://tixati.com/specs/bittorrent/v3.1

---

## The Trust Gap (2008–2026)

This is the gap Weightless exists to fill.

### The problem

BitTorrent verifies **integrity** (are the bytes correct?) but not **provenance** (who published this?). SHA-1/SHA-256 piece hashes prove the downloaded data matches the torrent metadata. But nothing proves who created the metadata, or whether it's been tampered with before you received it.

In 2026, this matters more than ever. AI model weights (70GB+), scientific datasets (100GB+), and genomic data are distributed via torrents. When you download a model's weights, you're trusting that the magnet link you found points to the real thing. There's no cryptographic proof.

### What was tried

| Year | Approach | Problem |
|------|----------|---------|
| 2008 | **BEP 35** — RSA/X.509 torrent signing | Too heavy. PKI dependency. Never adopted. |
| 2014 | **BEP 44** — Ed25519 DHT mutable items | Signs DHT entries, not torrents. Requires DHT. |
| 2016 | **BEP 46** — Updatable torrents via DHT | Decentralized identity, but v1 only, no metadata, requires DHT. |

### What Weightless proposes (Milestone 3)

**Ed25519 torrent signing** — the same primitive BEP 44 uses, applied to torrent metadata:
- Publisher generates Ed25519 keypair (`wl keygen`)
- Signs over both v1 and v2 info-hashes (hybrid support)
- Signature + public key stored in registry alongside torrent metadata
- Verification on download — CLI, server, or browser (WASM)
- Key format: raw 32 bytes, BEP 44 compatible (forward-compatible with DHT identity)

**The unified identity model:** One Ed25519 keypair works across:
1. Static torrent signing (who published this?)
2. Registry authentication (sign-on-register)
3. DHT mutable items (BEP 44/46 — future)
4. Browser verification (WASM)

This isn't new cryptography — it's applying existing, community-accepted primitives to a use case where no adopted solution exists.

---

## The Ecosystem Map (2026)

### Active Protocol Implementations

| Project | Language | Role | Notes |
|---------|----------|------|-------|
| **libtorrent** | C++ | Engine | Powers qBittorrent, Deluge. BEP 52 support. Arvid Norberg. |
| **Transmission** | C | Client | v4 supports hybrid. Clean, lightweight. |
| **qBittorrent** | C++ (libtorrent) | Client | Most popular open-source client. |
| **Deluge** | Python (libtorrent) | Client | Plugin-based, extensible. |
| **Tixati** | C++ (custom) | Client | Closed-source. v3/v3.1 originator. |
| **BiglyBT** | Java | Client | Fork of Vuze/Azureus. Native v2. |
| **WebTorrent** | JavaScript | Browser client | WebRTC-based P2P. v1 only. |
| **anacrolix/torrent** | Go | Library | Full-featured but heavy (CGO optional, many deps). |
| **Opentracker** | C | Tracker | Fast, minimal. No metadata, no auth. |
| **Chihaya** | Go | Tracker | Extensible, private-community focused. |
| **Weightless** | Go | Tracker + Library + CLI | Hybrid v1+v2, registry, provenance, zero-CGO. |

### Where Weightless is unique

No existing project combines:
- Hybrid v1+v2 at both creation and tracker level
- A registry with structured metadata (name, license, category, tags)
- Ed25519 provenance (BEP 44-compatible keys)
- A WASM module for browser-side publishing
- Pure Go, zero-CGO, embeddable as a library
- Registry-only tracking (anti-abuse by design)

---

## Quick Reference: All BEPs Relevant to Weightless

| BEP | Title | Status | Weightless |
|-----|-------|--------|------------|
| 3 | The BitTorrent Protocol | Final | ✅ Tracker + torrent creation |
| 5 | DHT Protocol (Kademlia) | Accepted | ❌ Future roadmap |
| 7 | IPv6 Tracker Extension | Draft | ✅ Supported |
| 9 | Metadata Exchange | Draft | 🔨 Milestone 1 |
| 10 | Extension Protocol | Draft | 🔨 Milestone 1 |
| 11 | Peer Exchange (PEX) | Accepted | ❌ Future |
| 23 | Compact Peer Lists | Accepted | ✅ Implemented |
| 35 | Torrent Signing (RSA/X.509) | Draft | 🔄 Replacing with Ed25519 (M3) |
| 44 | DHT Mutable Items (Ed25519) | Accepted | 🔑 Key format adopted (M3) |
| 46 | Updatable Torrents via DHT | Draft | ❌ Future roadmap |
| 48 | Scrape | Accepted | ✅ Implemented |
| 52 | BitTorrent v2 (SHA-256) | Draft | ✅ Full hybrid v1+v2 |
| — | Tixati v3/v3.1 | Tixati spec | 👀 Watching, tracker-compatible |

Legend: ✅ Done | 🔨 Grant-funded | 🔄 Proposing alternative | 🔑 Key format only | ❌ Not yet | 👀 Monitoring

---

*Document prepared 2026-03-29. Based on the Weightless codebase and public BEP specifications.*

*Sources:*
- *[BEP Index](https://www.bittorrent.org/beps/bep_0000.html)*
- *[The story of Bram Cohen](https://www.xda-developers.com/the-story-of-bram-cohen-and-the-bittorrent-protocol/)*
- *[libtorrent blog — BitTorrent v2](https://blog.libtorrent.org/2020/09/bittorrent-v2/)*
- *[Tixati v3 spec](https://tixati.com/specs/bittorrent/v3)*
- *[BitTorrent Wikipedia](https://en.wikipedia.org/wiki/BitTorrent)*
