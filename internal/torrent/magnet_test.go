package torrent

import (
	"testing"
)

func TestParseMagnetHybrid(t *testing.T) {
	t.Parallel()
	uri := "magnet:?xt=urn:btih:abc123def456&xt=urn:btmh:1220fedcba9876543210&dn=TestFile&tr=http://tracker:8080/announce"
	m, err := ParseMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	if m.InfoHashV1 != "abc123def456" {
		t.Errorf("v1 hash = %q, want abc123def456", m.InfoHashV1)
	}
	if m.InfoHashV2 != "fedcba9876543210" {
		t.Errorf("v2 hash = %q, want fedcba9876543210", m.InfoHashV2)
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
	uri := "magnet:?xt=urn:btih:AABBCCDD&dn=V1Only"
	m, err := ParseMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	if m.InfoHashV1 != "aabbccdd" {
		t.Errorf("v1 hash = %q, want aabbccdd", m.InfoHashV1)
	}
	if m.InfoHashV2 != "" {
		t.Errorf("v2 hash should be empty, got %q", m.InfoHashV2)
	}
	if m.BestHash() != "aabbccdd" {
		t.Errorf("BestHash = %q, want aabbccdd", m.BestHash())
	}
}

func TestParseMagnetV2Only(t *testing.T) {
	t.Parallel()
	uri := "magnet:?xt=urn:btmh:1220abcdef1234567890"
	m, err := ParseMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	if m.InfoHashV1 != "" {
		t.Errorf("v1 hash should be empty, got %q", m.InfoHashV1)
	}
	if m.InfoHashV2 != "abcdef1234567890" {
		t.Errorf("v2 hash = %q, want abcdef1234567890", m.InfoHashV2)
	}
	if m.BestHash() != "abcdef1234567890" {
		t.Errorf("BestHash should prefer v2")
	}
}

func TestParseMagnetBestHashPrefersV2(t *testing.T) {
	t.Parallel()
	uri := "magnet:?xt=urn:btih:v1hash&xt=urn:btmh:1220v2hash"
	m, err := ParseMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	if m.BestHash() != "v2hash" {
		t.Errorf("BestHash should prefer v2, got %q", m.BestHash())
	}
}

func TestParseMagnetMultipleTrackers(t *testing.T) {
	t.Parallel()
	uri := "magnet:?xt=urn:btih:abc&tr=http://one/announce&tr=http://two/announce"
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
	uri := "magnet:?xt=urn:btih:abc123"
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
	uri := "magnet:?xt=urn:btih:AABBCCDD"
	m, err := ParseMagnet(uri)
	if err != nil {
		t.Fatal(err)
	}
	if m.InfoHashV1 != "aabbccdd" {
		t.Errorf("expected lowercase hash, got %q", m.InfoHashV1)
	}
}
