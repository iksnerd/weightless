package torrent

import (
	"testing"
)

// Valid-length hex fixtures: v1 is 40 hex chars (20-byte SHA-1), v2 is 64 (32-byte SHA-256).
const (
	v1Hex      = "0123456789abcdef0123456789abcdef01234567"
	v1HexUpper = "0123456789ABCDEF0123456789ABCDEF01234567"
	v2Hex      = "fedcba9876543210fedcba9876543210fedcba9876543210fedcba9876543210"
)

func TestParseMagnetHybrid(t *testing.T) {
	t.Parallel()
	uri := "magnet:?xt=urn:btih:" + v1Hex + "&xt=urn:btmh:1220" + v2Hex + "&dn=TestFile&tr=http://tracker:8080/announce"
	m, err := ParseMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	if m.InfoHashV1 != v1Hex {
		t.Errorf("v1 hash = %q, want %s", m.InfoHashV1, v1Hex)
	}
	if m.InfoHashV2 != v2Hex {
		t.Errorf("v2 hash = %q, want %s", m.InfoHashV2, v2Hex)
	}
	if m.DisplayName != "TestFile" {
		t.Errorf("display name = %q, want TestFile", m.DisplayName)
	}
	if len(m.Trackers) != 1 || m.Trackers[0] != "http://tracker:8080/announce" {
		t.Errorf("trackers = %v", m.Trackers)
	}
}

func TestParseMagnetV1Only(t *testing.T) {
	t.Parallel()
	uri := "magnet:?xt=urn:btih:" + v1Hex + "&dn=V1Only"
	m, err := ParseMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	if m.InfoHashV1 != v1Hex {
		t.Errorf("v1 hash = %q, want %s", m.InfoHashV1, v1Hex)
	}
	if m.InfoHashV2 != "" {
		t.Errorf("v2 hash should be empty, got %q", m.InfoHashV2)
	}
	if m.BestHash() != v1Hex {
		t.Errorf("BestHash = %q, want %s", m.BestHash(), v1Hex)
	}
}

func TestParseMagnetV2Only(t *testing.T) {
	t.Parallel()
	uri := "magnet:?xt=urn:btmh:1220" + v2Hex
	m, err := ParseMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	if m.InfoHashV1 != "" {
		t.Errorf("v1 hash should be empty, got %q", m.InfoHashV1)
	}
	if m.InfoHashV2 != v2Hex {
		t.Errorf("v2 hash = %q, want %s", m.InfoHashV2, v2Hex)
	}
	if m.BestHash() != v2Hex {
		t.Errorf("BestHash should prefer v2")
	}
}

func TestParseMagnetBestHashPrefersV2(t *testing.T) {
	t.Parallel()
	uri := "magnet:?xt=urn:btih:" + v1Hex + "&xt=urn:btmh:1220" + v2Hex
	m, err := ParseMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	if m.BestHash() != v2Hex {
		t.Errorf("BestHash should prefer v2, got %q", m.BestHash())
	}
}

func TestParseMagnetMultipleTrackers(t *testing.T) {
	t.Parallel()
	uri := "magnet:?xt=urn:btih:" + v1Hex + "&tr=http://one/announce&tr=http://two/announce"
	m, err := ParseMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.Trackers) != 2 {
		t.Errorf("expected 2 trackers, got %d", len(m.Trackers))
	}
}

func TestParseMagnetNoHash(t *testing.T) {
	t.Parallel()
	_, err := ParseMagnet("magnet:?dn=NoHash&tr=http://tracker/announce")
	if err == nil {
		t.Error("expected error for magnet with no hash")
	}
}

func TestParseMagnetNotMagnet(t *testing.T) {
	t.Parallel()
	_, err := ParseMagnet("http://example.com")
	if err == nil {
		t.Error("expected error for non-magnet URI")
	}
}

func TestParseMagnetNoDisplayName(t *testing.T) {
	t.Parallel()
	uri := "magnet:?xt=urn:btih:" + v1Hex
	m, err := ParseMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	if m.DisplayName != "" {
		t.Errorf("expected empty display name, got %q", m.DisplayName)
	}
}

func TestParseMagnetCaseInsensitive(t *testing.T) {
	t.Parallel()
	// Hashes should be lowercased
	uri := "magnet:?xt=urn:btih:" + v1HexUpper
	m, err := ParseMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	if m.InfoHashV1 != v1Hex {
		t.Errorf("expected lowercase hash, got %q", m.InfoHashV1)
	}
}

func TestParseMagnetRejectsMalformedHash(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		uri  string
	}{
		{"v1 too short", "magnet:?xt=urn:btih:abc123"},
		{"v1 non-hex", "magnet:?xt=urn:btih:zzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzzz"},
		{"v2 too short", "magnet:?xt=urn:btmh:1220abcdef"},
		{"v2 non-hex", "magnet:?xt=urn:btmh:1220" + "g123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if _, err := ParseMagnet(tt.uri); err == nil {
				t.Errorf("ParseMagnet(%q) = nil error, want rejection", tt.uri)
			}
		})
	}
}
