package tracker

import (
	"errors"
	"fmt"
	"net/url"
	"strconv"
)

// RecognizeAnnounce is the LangSec recognizer for BEP 3/7/52 announce requests.
// It fully validates the input grammar before any business logic runs — no
// silent fallbacks. Inspired by Sassaman & Patterson, "The Science of
// Insecurity": recognize completely, then execute.

// PeerEvent is the typed BEP 3 announce event.
type PeerEvent uint8

const (
	EventNone PeerEvent = iota
	EventStarted
	EventStopped
	EventCompleted
)

// AnnounceParams is the fully-recognized announce request. Once constructed
// successfully, every field is bounded and typed; no further parsing is needed
// downstream.
type AnnounceParams struct {
	InfoHash    [32]byte // padded — actual bytes in [:InfoHashLen]
	InfoHashLen int      // 20 (v1 SHA-1) or 32 (v2 SHA-256)
	PeerID      [20]byte
	Port        uint16
	Uploaded    int64
	Downloaded  int64
	Left        int64
	Event       PeerEvent
	NumWant     int  // -1 = not specified, caller applies default
	Compact     bool // BEP 23 — default true if absent
}

const (
	// MaxNumWant bounds the requested peer count to prevent absurd values.
	MaxNumWant = 1000
)

// InfoHashHex returns the recognized info_hash as a lowercase hex string —
// the canonical form used for DB lookups and registry keys.
func (p AnnounceParams) InfoHashHex() string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, p.InfoHashLen*2)
	for i := 0; i < p.InfoHashLen; i++ {
		out[i*2] = hexdigits[p.InfoHash[i]>>4]
		out[i*2+1] = hexdigits[p.InfoHash[i]&0x0f]
	}
	return string(out)
}

// PeerIDString returns the peer_id as a string for use as a map key.
func (p AnnounceParams) PeerIDString() string {
	return string(p.PeerID[:])
}

// RecognizeAnnounce parses and validates announce query parameters.
// Returns a typed error on the first violation; the caller surfaces the
// reason in a BEP 3 bencoded failure response.
func RecognizeAnnounce(q url.Values) (AnnounceParams, error) {
	var p AnnounceParams

	// info_hash — required, 20 bytes (v1) or 32 bytes (v2)
	hashRaw := q.Get("info_hash")
	if hashRaw == "" {
		return p, errors.New("missing info_hash")
	}
	switch len(hashRaw) {
	case 20:
		p.InfoHashLen = 20
	case 32:
		p.InfoHashLen = 32
	default:
		return p, fmt.Errorf("info_hash must be 20 or 32 bytes, got %d", len(hashRaw))
	}
	copy(p.InfoHash[:], hashRaw)

	// peer_id — required, exactly 20 bytes per BEP 3
	peerID := q.Get("peer_id")
	if peerID == "" {
		return p, errors.New("missing peer_id")
	}
	if len(peerID) != 20 {
		return p, fmt.Errorf("peer_id must be exactly 20 bytes, got %d", len(peerID))
	}
	copy(p.PeerID[:], peerID)

	// port — required, 1..65535
	portStr := q.Get("port")
	if portStr == "" {
		return p, errors.New("missing port")
	}
	port, err := strconv.ParseUint(portStr, 10, 16)
	if err != nil {
		return p, fmt.Errorf("invalid port: must be 1-65535")
	}
	if port == 0 {
		return p, errors.New("invalid port: must be 1-65535")
	}
	p.Port = uint16(port)

	// uploaded — optional in practice; if present must parse and be >= 0
	if v := q.Get("uploaded"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return p, fmt.Errorf("invalid uploaded: must be a non-negative integer")
		}
		p.Uploaded = n
	}

	// downloaded
	if v := q.Get("downloaded"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return p, fmt.Errorf("invalid downloaded: must be a non-negative integer")
		}
		p.Downloaded = n
	}

	// left
	if v := q.Get("left"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return p, fmt.Errorf("invalid left: must be a non-negative integer")
		}
		p.Left = n
	}

	// event — optional enum
	switch q.Get("event") {
	case "":
		p.Event = EventNone
	case "started":
		p.Event = EventStarted
	case "stopped":
		p.Event = EventStopped
	case "completed":
		p.Event = EventCompleted
	default:
		return p, fmt.Errorf("invalid event: must be started, stopped, or completed")
	}

	// numwant — optional, 0..MaxNumWant
	p.NumWant = -1
	if v := q.Get("numwant"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 0 || n > MaxNumWant {
			return p, fmt.Errorf("invalid numwant: must be 0-%d", MaxNumWant)
		}
		p.NumWant = n
	}

	// compact — BEP 23, default 1
	p.Compact = true
	if v := q.Get("compact"); v != "" {
		switch v {
		case "0":
			p.Compact = false
		case "1":
			p.Compact = true
		default:
			return p, fmt.Errorf("invalid compact: must be 0 or 1")
		}
	}

	return p, nil
}
