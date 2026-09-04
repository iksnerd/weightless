package client

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/zeebo/bencode"
)

func TestUnpackPeers(t *testing.T) {
	t.Parallel()
	// 2 peers: 1.2.3.4:6881 and 5.6.7.8:8080
	peers := []byte{
		1, 2, 3, 4, 0x1a, 0xe1, // 6881 = 0x1ae1
		5, 6, 7, 8, 0x1f, 0x90, // 8080 = 0x1f90
	}

	addrs, err := unpackPeers(peers, net.IPv4len)
	if err != nil {
		t.Fatalf("unpackPeers failed: %v", err)
	}

	if len(addrs) != 2 {
		t.Errorf("expected 2 peers, got %d", len(addrs))
	}
	if addrs[0] != "1.2.3.4:6881" {
		t.Errorf("expected 1.2.3.4:6881, got %s", addrs[0])
	}
	if addrs[1] != "5.6.7.8:8080" {
		t.Errorf("expected 5.6.7.8:8080, got %s", addrs[1])
	}
}

func TestUnpackPeersIPv6(t *testing.T) {
	t.Parallel()
	// One IPv6 peer: [::1]:6881 — 16 address bytes + 2 port bytes.
	peers := make([]byte, 0, 18)
	peers = append(peers, net.ParseIP("::1").To16()...)
	peers = append(peers, 0x1a, 0xe1) // 6881

	addrs, err := unpackPeers(peers, net.IPv6len)
	if err != nil {
		t.Fatalf("unpackPeers (v6) failed: %v", err)
	}
	if len(addrs) != 1 {
		t.Fatalf("expected 1 peer, got %d", len(addrs))
	}
	if addrs[0] != "[::1]:6881" {
		t.Errorf("expected [::1]:6881, got %s", addrs[0])
	}
}

func TestUnpackPeersInvalidLength(t *testing.T) {
	t.Parallel()
	peers := []byte{1, 2, 3, 4, 5} // 5 bytes, not a multiple of 6
	_, err := unpackPeers(peers, net.IPv4len)
	if err == nil {
		t.Error("expected error for invalid length")
	}
}

func TestAnnounce(t *testing.T) {
	t.Parallel()
	// Mock tracker
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Check required query params
		q := r.URL.Query()
		if q.Get("info_hash") == "" || q.Get("peer_id") == "" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}

		// Return a compact peer list with one peer
		peerBytes := make([]byte, 6)
		copy(peerBytes[0:4], net.ParseIP("127.0.0.1").To4())
		binary.BigEndian.PutUint16(peerBytes[4:6], 6881)

		resp := map[string]interface{}{
			"interval": 1800,
			"peers":    string(peerBytes),
		}
		data, _ := bencode.EncodeBytes(resp)
		w.Write(data)
	}))
	defer server.Close()

	peers, err := Announce(context.Background(), server.URL, AnnounceOptions{
		InfoHash: "fakehash123456789012",
		PeerID:   "-WL0001-123456789012",
		Port:     6881,
		Left:     1024,
		Event:    EventStarted,
	})
	if err != nil {
		t.Fatalf("Announce failed: %v", err)
	}

	if len(peers) != 1 {
		t.Errorf("expected 1 peer, got %d", len(peers))
	}
	if peers[0] != "127.0.0.1:6881" {
		t.Errorf("expected 127.0.0.1:6881, got %s", peers[0])
	}
}

func TestAnnounceFailure(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := map[string]interface{}{
			"failure reason": "test failure",
		}
		data, _ := bencode.EncodeBytes(resp)
		w.Write(data)
	}))
	defer server.Close()

	_, err := Announce(context.Background(), server.URL, AnnounceOptions{InfoHash: "hash", PeerID: "peer", Port: 6881})
	if err == nil {
		t.Fatal("expected error from tracker failure")
	}
	if err.Error() != "tracker failure: test failure" {
		t.Errorf("unexpected error message: %v", err)
	}
}

// The tracker's usage accounting and completion counter key off these
// fields, so every one of them must reach the wire verbatim.
func TestAnnounceSendsStatsAndEvent(t *testing.T) {
	t.Parallel()
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		data, _ := bencode.EncodeBytes(map[string]interface{}{"interval": 1800, "peers": ""})
		w.Write(data)
	}))
	defer server.Close()

	_, err := Announce(context.Background(), server.URL, AnnounceOptions{
		InfoHash:   "fakehash123456789012",
		PeerID:     "-WL0001-123456789012",
		Port:       6881,
		Uploaded:   0,
		Downloaded: 4096,
		Left:       0,
		Event:      EventCompleted,
	})
	if err != nil {
		t.Fatalf("Announce: %v", err)
	}
	want := map[string]string{
		"uploaded":   "0",
		"downloaded": "4096",
		"left":       "0",
		"event":      "completed",
		"port":       "6881",
		"compact":    "1",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("%s = %q, want %q", k, got.Get(k), v)
		}
	}
}

// A periodic announce carries no event key at all (BEP 3), rather than an
// empty one that some trackers reject.
func TestAnnounceOmitsEmptyEvent(t *testing.T) {
	t.Parallel()
	var got url.Values
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		data, _ := bencode.EncodeBytes(map[string]interface{}{"interval": 1800, "peers": ""})
		w.Write(data)
	}))
	defer server.Close()

	if _, err := Announce(context.Background(), server.URL, AnnounceOptions{InfoHash: "h", PeerID: "p", Port: 1}); err != nil {
		t.Fatalf("Announce: %v", err)
	}
	if _, present := got["event"]; present {
		t.Errorf("event key present on periodic announce: %q", got.Get("event"))
	}
}
