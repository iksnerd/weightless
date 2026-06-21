# RFC 0001: Lightweight Hybrid Torrent Signing with Ed25519

**Status:** draft (proposed; not yet implemented)
**Scope:** A publisher-signature scheme that binds an Ed25519 key to a hybrid v1+v2
torrent's content identity, verifiable standalone, without PKI, DHT, or a tracker.

## Summary

A publisher signs the *content identity* of a hybrid torrent (its v1 SHA-1 and v2 SHA-256
info hashes) with an Ed25519 key. The 64-byte signature and the 32-byte public key are
stored in the registry record and, optionally, in the `.torrent` file outside the info
dict. Any party that has the torrent can recompute the signed statement from the info
hashes it already holds and verify the signature offline. This gives "who published this,
and is it what they claim?" a cryptographic answer, using primitives already proven in the
BitTorrent ecosystem (Ed25519, the BEP 44 key format), without the X.509 machinery that
sank BEP 35. The public prior-art summary is §9 below; a fuller analysis is kept as an
internal companion note (`BEP_PROVENANCE_ANALYSIS`).

## Motivation

BitTorrent has no adopted standard for publisher verification. BEP 35 (2008) proposed it
with RSA and X.509 and was never implemented by a major client; the PKI dependency made it
hostile to CLI, embedded, and browser use. No alternative has shipped since. The need has
grown: torrents now carry model weights and research datasets, where a recipient cannot
inspect the payload and must rely on provenance. Ed25519 is already in the ecosystem
(BEP 44 mutable DHT items use it), so the gap is not a missing primitive. It is a missing,
deployable *scheme* for signing the torrent itself. This document specifies that scheme.

## Conventions and terminology

The key words "MUST", "MUST NOT", "REQUIRED", "SHOULD", "SHOULD NOT", "RECOMMENDED", "MAY",
and "OPTIONAL" in this document are to be interpreted as described in BCP 14 [RFC 2119]
[RFC 8174] when, and only when, they appear in all capitals.

- **Info dict**: the bencoded `info` dictionary of a torrent. Its SHA-1 is the **v1 info
  hash** (`btih`, 20 bytes); its SHA-256 is the **v2 info hash** (`btmh`, 32 bytes). A
  hybrid torrent has both; a v1-only or v2-only torrent has one.
- **Publisher key**: an Ed25519 public key, 32 bytes, raw (not DER), identical in format to
  a BEP 44 key.
- **Signed statement**: the canonical bencoded byte string defined in §4.1 that the
  signature covers.
- **Signature**: a 64-byte Ed25519 signature over the signed statement.

## The model

The signature binds three things: the publisher's key, the content's v1 identity, and the
content's v2 identity. Binding *both* hashes is the load-bearing decision. A hybrid torrent
has two content identifiers, and signing only one would let an attacker who can manipulate
the other (notably the SHA-1 v1 hash, which is collision-prone) present mismatched content
under a valid signature. Covering the SHA-256 v2 hash in the same statement means a v1
collision alone does not yield a forgery: the attacker would also have to match the signed
SHA-256, which is infeasible. For a v1-only or v2-only torrent the statement carries
whichever hash exists.

Verification is local and trustless of any third party: the verifier already has the info
hashes (from the torrent or the magnet), recomputes the signed statement, and checks the
Ed25519 signature against the claimed public key. No CA, no DHT lookup, no tracker round
trip is required.

The scheme deliberately separates two questions it does and does not answer. It answers
"was this content signed by the holder of key K?" It does *not* answer "is key K the real
Stanford / Meta / EleutherAI?" That second question (identity binding) is out of scope here
and is addressed operationally by the registry's verified-publisher flag or by out-of-band
key distribution. See Security Considerations.

## What it standardizes

### 4.1 The signed statement

The signed statement is a canonically bencoded dictionary. Keys MUST be sorted in
lexicographic (byte-wise) order as required by BEP 3 bencoding, which the Weightless
encoder already enforces. The statement is the concatenation of a domain-separation prefix
and the bencoded dictionary:

```
signing_input = "weightless:torrent-sig:v1" || bencode(statement_dict)
```

The domain-separation prefix is a fixed ASCII string with no length prefix and no
terminator; it ensures a Weightless signature can never be replayed as a signature in
another protocol that signs raw bencode.

`statement_dict` has the following keys. A key MUST be present exactly when its hash exists
for the torrent; at least one of `btih`/`btmh` MUST be present.

| key | bencode type | bytes | meaning |
|-----|--------------|-------|---------|
| `alg` | byte string | — | signature algorithm; MUST be `"ed25519"` in this version |
| `btih` | byte string | 20 | raw v1 info hash (SHA-1), present iff the torrent is v1 or hybrid |
| `btmh` | byte string | 32 | raw v2 info hash (SHA-256), present iff the torrent is v2 or hybrid |
| `pub` | byte string | 32 | the publisher's raw Ed25519 public key |
| `v` | integer | — | statement format version; MUST be `1` |

Including `pub` inside the signed statement binds the key to the signature, so a signature
cannot be re-presented under a different claimed key.

Wire layout of the statement dictionary (raw, before the signature is computed):

```
d
  3:alg 7:ed25519
  4:btih 20:<20 raw bytes>     ; omitted for a v2-only torrent
  4:btmh 32:<32 raw bytes>     ; omitted for a v1-only torrent
  3:pub  32:<32 raw bytes>
  1:v    i1e
e
```

A signer MUST construct `statement_dict` from these fields only. A verifier MUST ignore any
torrent-level signature whose statement contains keys it does not recognize for version 1
(strict receiver; see §8). Future versions MAY add keys by incrementing `v`.

### 4.2 Signature object

A signature is represented as a bencoded dictionary so it can travel inside a `.torrent`
file or be serialized to JSON for the registry:

| key | type | meaning |
|-----|------|---------|
| `alg` | byte string | MUST equal the statement's `alg` |
| `pub` | byte string (32) | MUST equal the statement's `pub` |
| `sig` | byte string (64) | the Ed25519 signature over `signing_input` |
| `v` | integer | MUST equal the statement's `v` |

### 4.3 Placement

**In the torrent file (OPTIONAL).** A signed `.torrent` MAY carry a top-level `signatures`
key, a list of signature objects (§4.2), placed *outside* the `info` dict. Because the
signatures sit outside `info`, both info hashes are unchanged, so existing clients that do
not understand the key MUST ignore it (BEP 3 / BEP 52 dictionaries already require ignoring
unknown keys), and the torrent remains byte-identical in swarm terms to its unsigned form.
A torrent MAY carry more than one signature (co-signing).

**In the registry (REQUIRED for registry-verified provenance).** A registry record for a
signed torrent carries `publisher_key` (hex of the 32-byte key) and `signature` (hex of the
64-byte signature). The registry MAY also expose a `verified` boolean asserting that the
operator has, out of band, confirmed the key belongs to the named publisher.

### 4.4 Signing and verification procedures

Signing:

1. Compute the torrent's `btih` and/or `btmh`.
2. Build `statement_dict` (§4.1) with the publisher's public key.
3. Compute `signing_input = prefix || bencode(statement_dict)`.
4. `sig = Ed25519-Sign(private_key, signing_input)`.
5. Emit the signature object (§4.2) into the registry record and/or the torrent.

Verification:

1. Obtain the torrent's `btih`/`btmh` from the torrent or magnet.
2. Read the candidate signature object; let its `pub`, `sig`, `alg`, `v` be the claimed
   values.
3. If `alg != "ed25519"` or `v != 1`, the verifier MUST reject (unknown version).
4. Reconstruct `statement_dict` from the *locally computed* info hashes plus the claimed
   `pub`, then `signing_input`.
5. Return `Ed25519-Verify(pub, signing_input, sig)`.
6. A `true` result proves only that the holder of `pub` signed *this* content. Whether to
   trust `pub` is a separate decision (§8).

A verifier MUST reconstruct the statement from hashes it computed itself, never from hash
values supplied alongside the signature. Otherwise an attacker could supply a matching
(hash, signature) pair for content the verifier never actually checked.

## Conformance and testing

An implementation is conformant if, given the test vectors below, it produces the listed
signature when signing and accepts it (and rejects the listed negative cases) when
verifying. The reference implementation MUST ship a conformance corpus containing, at
minimum:

- A hybrid torrent: a known Ed25519 seed, known `btih` and `btmh`, the expected 64-byte
  signature (hex), and the expected `signing_input` (hex) so a third party can check the
  canonical encoding independently of the crypto.
- A v1-only torrent and a v2-only torrent, exercising the omitted-key cases.
- Negative cases that MUST fail verification: flipped `btih`, flipped `btmh`, wrong `pub`,
  truncated `sig`, `v != 1`, an unknown extra key in the statement.

These vectors do not exist yet (the scheme is unimplemented); producing them is part of the
reference implementation work.

## Security considerations

- **Threat model.** The adversary can serve torrents, magnets, and registry responses, and
  can attempt to substitute content under a publisher's name. The adversary cannot obtain
  the publisher's private key.
- **What a valid signature proves, and what it does not.** It proves the content's info
  hashes were signed by the holder of `pub`. It does not prove `pub`'s real-world identity.
  Identity binding is out of scope and MUST be handled by trust-on-first-use, the registry
  `verified` flag, or out-of-band key exchange. Implementations MUST NOT present a valid
  signature as "verified publisher X" unless identity was established separately.
- **SHA-1 weakness, and why hybrid signing mitigates it.** The v1 info hash is SHA-1, which
  is not collision-resistant. Because the statement also binds the SHA-256 v2 hash, a SHA-1
  collision alone cannot produce content that verifies; the attacker would additionally
  need a SHA-256 preimage/collision. For v1-only torrents this mitigation is unavailable,
  and verifiers SHOULD surface lower assurance for a v1-only signature.
- **No rollback or revocation in this version.** A signature does not stop an adversary from
  re-serving an older signed version, and there is no key-revocation mechanism. Rollback
  protection (monotonic sequence numbers) is a property of the DHT mutable-item path
  (BEP 44/46) and is deliberately deferred; see Open questions.
- **Domain separation.** The fixed prefix prevents a Weightless signature from being
  accepted by, or forged from, any other protocol that signs bencoded data.
- **Strict receiver.** Verification fails closed on any malformed, truncated, or
  unknown-version input. The scheme does not "guess" or accept a superset, in line with the
  modern reading of the robustness principle for security-relevant parsing.

## Drawbacks and non-goals

**Drawbacks.**
- Adds a key-management burden on publishers (generate, store, and protect a private key).
- A signature without an identity layer can give false confidence if a UI overstates it;
  the spec mitigates this with the §8 presentation requirement, but it is a real risk.
- Two storage locations (registry and torrent) mean two code paths to keep consistent.

**Non-goals.**
- Not a PKI or identity system. No CA, no name binding at the protocol level.
- Not revocation. A compromised key cannot be retired within this scheme.
- Not rollback protection. Deferred to the DHT phase.
- Not content encryption or access control. Signing is integrity and origin only.

## Alternatives and prior art

Summarized here; a fuller comparison is kept in the internal companion note
`BEP_PROVENANCE_ANALYSIS`.

- **BEP 35 (RSA + X.509).** Same goal, heavier primitives, PKI dependency, no client
  adoption, no browser path. This RFC keeps the goal and replaces the cryptography.
- **BEP 44 + 46 (Ed25519 DHT mutable items).** Signs a *DHT item that points at* an info
  hash, not the torrent's content identity, and is v1-only and DHT-dependent. This RFC signs
  the content identity directly, works without a DHT, and is hybrid-aware. The 32-byte key
  format is shared, so a Weightless key is forward-compatible with the DHT path.
- **Do nothing / trust the host.** The status quo for Academic Torrents and for raw magnet
  links. Provides no origin assurance, which is precisely the gap.

## Compatibility and migration

Signatures live outside the info dict, so adding one does not change either info hash and an
unsigned and signed copy of the same content occupy the same swarm. Clients that predate
this RFC ignore the `signatures` key and the registry fields, so deployment is incremental:
a publisher gains verifiability the moment they sign, with no coordinated upgrade required
of downloaders (RFC 5218 incremental-deployability and aligned-incentives factors: the
publisher who pays the small signing cost is the party whose content gains trust).
Versioning is by the statement `v` field; a future `v: 2` can add statement keys while
`v: 1` verifiers continue to reject what they do not understand.

## Reference implementation

None of the following is built yet. All are proposed, and are listed here to keep the
proposed/shipped line bright:

- **Proposed:** `wl keygen` to generate an Ed25519 keypair.
- **Proposed:** `wl create --sign` to compute and attach a signature (registry + optional
  in-torrent).
- **Proposed:** signature verification in `wl get`, before and/or after download.
- **Proposed:** registry storage of `publisher_key` / `signature` and an operator-set
  `verified` flag.
- **Proposed:** a WASM verifier so the same check runs in the browser (Ed25519 verification
  is small and dependency-free in JS/WASM, unlike X.509).
- **Proposed:** the conformance corpus of §5.

When each lands, it moves from "proposed" to a shipped entry here with a path.

## Open questions

- **Rollback / versioning of *which content* a publisher endorses.** Solved by BEP 44/46
  sequence numbers in a future DHT phase; should this RFC reserve a statement key for a
  sequence number now, or leave it entirely to that phase?
- **Multi-signature semantics.** When a torrent carries several signatures, is that
  co-signing (all valid) or alternatives (any valid)? The current text allows a list without
  defining quorum.
- **Key revocation.** Out of scope here; is a registry-level revocation list acceptable, or
  does that re-centralize the trust the scheme tries to distribute?
- **Should the in-torrent `signatures` key be standardized for upstream BEP submission**, or
  kept registry-only until the scheme has adoption? (Phase 2 of the provenance strategy.)
```
