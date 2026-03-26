# BitTorrent Provenance: BEP Landscape and Weightless Strategy

## The Problem

BitTorrent has no widely-adopted standard for answering: *"Who published this, and is it what they claim?"*

This matters now more than ever. When the commons shifts from code to data — model weights, datasets, genomic sequences — provenance becomes the critical trust layer. You can fork a GitHub repo and read the code. You can't eyeball a 70GB model weight file. You need cryptographic proof of origin.

## Existing BEPs — What Was Tried, What Failed

### BEP 35 — Torrent Signing (2008, Draft)

**What it does:** Embeds RSA signatures into `.torrent` files. A signing entity (identified by reverse-DNS, e.g., `com.bittorrent`) signs the info dictionary using RSA. The signature and an X.509 certificate are stored in a `signatures` key in the torrent file.

**Why it failed:**
- **RSA + X.509 is heavy.** Requires certificate chains, DER encoding, and a trust model based on certificate authorities. This is overkill for a torrent file and hostile to CLI/embedded use.
- **PKI dependency.** You need a certificate signed by a trusted CA, or users must manually trust your certificate. There's no lightweight path to "I trust this publisher's key."
- **No client adoption.** No major client (Transmission, qBittorrent, Deluge) ships with BEP 35 verification. The `ut-signing-tool` repo from BitTorrent Inc. exists but is unmaintained.
- **No browser path.** X.509 certificate handling in WASM/browser contexts is impractical.
- **Status:** Draft. Effectively dead. Mentioned in BEP 39 (feeds) but not implemented in practice.

**Reference:** https://www.bittorrent.org/beps/bep_0035.html

### BEP 44 — Storing Arbitrary Data in the DHT (2014, Accepted)

**What it does:** Enables storing immutable and mutable items in the BitTorrent DHT. Mutable items use **Ed25519** for signing — the publisher's 32-byte public key is the lookup key, and updates must include a monotonically increasing sequence number to prevent rollback attacks.

**Why it matters:**
- **Ed25519 is the right primitive.** 32-byte keys, 64-byte signatures, fast, no PKI, no certificates. This is the modern standard for signing (used by SSH, Signal, Tor, DNSSEC).
- **Already in the ecosystem.** libtorrent (the C++ library behind qBittorrent and Deluge) implements BEP 44. The key format is established.
- **Mutable items enable "follow a publisher."** You look up a public key in the DHT and get the latest info hash they've published. No tracker needed.

**Reference:** https://www.bittorrent.org/beps/bep_0044.html

### BEP 46 — Updating Torrents via DHT Mutable Items (2016, Draft)

**What it does:** Builds on BEP 44. A publisher stores `{ih: <20-byte infohash>}` as a mutable DHT item, signed with their Ed25519 key. Magnet links use the public key instead of the info hash: `magnet:?xs=urn:btpk:<public-key-hex>`. Clients resolve the magnet by looking up the key in the DHT, verifying the signature, and downloading the torrent at the referenced info hash.

**Why it matters:**
- **Decentralized publisher identity.** No registry, no tracker, no CA. The publisher *is* their public key.
- **Updatable torrents.** A publisher can update what a magnet link points to (e.g., new version of a dataset) without changing the link. Sequence numbers prevent rollback.
- **The "follow" primitive.** This is conceptually what Weightless's registry does centrally — BEP 46 does it via the DHT.

**Limitations:**
- Requires DHT support (Weightless doesn't implement DHT yet)
- v1 only (20-byte info hash) — no hybrid v1+v2 support
- No metadata beyond the info hash (no name, license, category, etc.)

**Reference:** https://bittorrent.org/beps/bep_0046.html

### BEP 39 — Updating Torrents via Feed URL (2012, Draft)

**What it does:** RSS-like feed mechanism for updating torrents. References BEP 35 for signing.

**Why it matters less:** HTTP-dependent, centralized feed URL, tied to BEP 35's failed signing model.

**Reference:** https://www.bittorrent.org/beps/bep_0039.html

---

## The Gap Weightless Fills

None of the existing BEPs solve the full problem:

| Requirement | BEP 35 | BEP 44+46 | Weightless (proposed) |
|------------|--------|-----------|----------------------|
| Sign a torrent at publish time | Yes (RSA/X.509) | No (signs DHT item, not torrent) | Yes (Ed25519, in registry + torrent file) |
| Verify on download | Requires CA trust chain | Requires DHT lookup | Yes (standalone signature verification) |
| Lightweight keys (no PKI) | No | Yes (Ed25519) | Yes (Ed25519, BEP 44-compatible key format) |
| Works without DHT | Yes | No | Yes |
| Works without tracker | No | Yes | Partial (registry-assisted + BEP 9 fallback) |
| Hybrid v1+v2 support | No (predates v2) | No (v1 only) | Yes |
| Structured metadata (name, license, tags) | No | No | Yes (registry data model) |
| Browser-verifiable | Impractical (X.509) | Possible but complex | Yes (Ed25519 in WASM) |
| Updatable (new versions) | No | Yes (seq numbers) | Yes (registry API) |

## Weightless Provenance Strategy

### Phase 1: Registry-Integrated Signing (Milestone 3)

- Publisher generates Ed25519 keypair via `wl keygen`
- `wl create --sign` signs the torrent's info dictionary and stores the signature + public key in the registry alongside the torrent metadata
- `wl get` verifies the signature before/after download
- The WASM module can verify signatures in the browser (Ed25519 is fast and simple in JS/WASM)
- **Key format:** Use the same 32-byte Ed25519 public key format as BEP 44. This means Weightless keys are forward-compatible with DHT-based identity if DHT support is added later.

### Phase 2: Formal BEP Submission (Milestone 4 / post-grant)

- Document the signing scheme as a new BEP: "Lightweight Torrent Signing with Ed25519"
- Position as the modern successor to BEP 35: same goal (sign torrents, verify publishers), better primitives (Ed25519 vs RSA, no PKI)
- Include hybrid v1+v2 support (sign over both info hashes)
- Reference BEP 44's key format for ecosystem compatibility

### Phase 3: DHT Integration (future roadmap)

- Implement BEP 44 mutable item storage
- Implement BEP 46 updatable torrents, extended for hybrid v1+v2 (32-byte info hashes)
- The same Ed25519 keypair used for registry signing works for DHT publishing
- This creates a decentralized fallback: even if the Weightless registry goes down, publishers can be discovered and verified via DHT

---

## Why This Matters for the NLnet Proposal

The provenance milestone applies existing, proven cryptography (Ed25519) to a use case the BitTorrent ecosystem hasn't solved:

1. **Addressing an unmet need.** BEP 35 proposed torrent signing in 2008 and was never adopted — likely because RSA/X.509/PKI was too heavy for the use case. No alternative has shipped since. The need for publisher verification has grown as torrents are increasingly used for distributing AI models and research datasets.

2. **Applying what already works.** Ed25519 is proven in the BitTorrent ecosystem (BEP 44). We apply the same primitive and key format to torrent signing — not inventing new cryptography, but composing existing standards to cover a gap.

3. **Documenting as an open specification.** The signing scheme will be documented publicly and submitted as a BEP draft for community review. Whether it achieves adoption depends on factors beyond this project (client maintainer interest, community buy-in), but the specification and reference implementation will exist for others to evaluate and build on.

4. **Browser-verifiable by design.** Ed25519 verification runs trivially in WASM, unlike BEP 35's X.509 certificate handling. This means provenance works everywhere the WASM module works.

### Honest scope
We are not claiming to set a new cryptographic standard. We are applying existing, well-established cryptography to a specific use case where no adopted solution exists. The contribution is the specification, the reference implementation, and the proof that it works across CLI, server, and browser — not the cryptographic primitive itself.

---

*Document prepared 2026-03-29. Informs Milestone 3 (Data Provenance) of the NLnet grant proposal.*
