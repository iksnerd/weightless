package client

import (
	"context"
	"encoding/binary"
	"net"
	"net/http"
	"net/http/httptest"
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

	addrs, err := unpackPeers(peers)
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

func TestUnpackPeersInvalidLength(t *testing.T) {
	t.Parallel()
	peers := []byte{1, 2, 3, 4, 5} // 5 bytes, not a multiple of 6
	_, err := unpackPeers(peers)
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

	peers, err := Announce(context.Background(), server.URL, "fakehash123456789012", "-WL0001-123456789012", 6881, 1024)
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

	_, err := Announce(context.Background(), server.URL, "hash", "peer", 6881, 0)
	if err == nil {
		t.Fatal("expected error from tracker failure")
	}
	if err.Error() != "tracker failure: test failure" {
		t.Errorf("unexpected error message: %v", err)
	}
}
