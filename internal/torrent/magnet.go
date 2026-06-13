package torrent

import (
	"fmt"
	"net/url"
	"strings"
)

// Magnet holds the parsed fields from a magnet URI.
type Magnet struct {
	InfoHashV1  string   // hex-encoded SHA-1 (from xt=urn:btih:...)
	InfoHashV2  string   // hex-encoded SHA-256 (from xt=urn:btmh:1220...)
	DisplayName string   // dn parameter
	Trackers    []string // tr parameters (announce URLs)
}

// BestHash returns the v2 hash if available, otherwise v1.
func (m Magnet) BestHash() string {
	if m.InfoHashV2 != "" {
		return m.InfoHashV2
	}
	return m.InfoHashV1
}

// ParseMagnet parses a magnet URI into its components.
// Supports hybrid v1+v2 magnets (BEP 50) with multiple xt parameters.
func ParseMagnet(uri string) (Magnet, error) {
	if !strings.HasPrefix(uri, "magnet:?") {
		return Magnet{}, fmt.Errorf("not a magnet URI")
	}

	query := uri[len("magnet:?"):]
	params, err := url.ParseQuery(query)
	if err != nil {
		return Magnet{}, fmt.Errorf("parse magnet query: %w", err)
	}

	var m Magnet

	for _, xt := range params["xt"] {
		switch {
		case strings.HasPrefix(xt, "urn:btih:"):
			v1 := strings.ToLower(xt[len("urn:btih:"):])
			// This implementation is hex-only (no base32): a v1 info hash is
			// the 40-char hex of a 20-byte SHA-1.
			if !isHex(v1, 40) {
				return Magnet{}, fmt.Errorf("invalid v1 info hash in magnet: %q", v1)
			}
			m.InfoHashV1 = v1
		case strings.HasPrefix(xt, "urn:btmh:1220"):
			// Multihash prefix 1220: 0x12 = SHA-256, 0x20 = 32 bytes, so the
			// remainder must be the 64-char hex of a 32-byte digest.
			v2 := strings.ToLower(xt[len("urn:btmh:1220"):])
			if !isHex(v2, 64) {
				return Magnet{}, fmt.Errorf("invalid v2 info hash in magnet: %q", v2)
			}
			m.InfoHashV2 = v2
		}
	}

	if m.InfoHashV1 == "" && m.InfoHashV2 == "" {
		return Magnet{}, fmt.Errorf("magnet URI has no info hash")
	}

	m.DisplayName = params.Get("dn")
	m.Trackers = params["tr"]

	return m, nil
}

// isHex reports whether s is exactly n lowercase hex characters.
func isHex(s string, n int) bool {
	if len(s) != n {
		return false
	}
	for _, c := range s {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}
