package tracker

import (
	"database/sql"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zeebo/bencode"
)

// v2Hash is a 32-byte hex string for BEP 52 info_hash in tests.
const v2Hash = "0123456789012345678901234567890101234567890123456789012345678901" // exactly 64 hex chars

// announceURL builds an announce URL with a binary info_hash (decoded from hex).
func buildAnnounceURL(hashHex, peerID, port string, extra ...string) string {
	bin, _ := hex.DecodeString(hashHex)
	u := "/announce?info_hash=" + url.QueryEscape(string(bin)) + "&peer_id=" + peerID + "&port=" + port
	for _, e := range extra {
		u += "&" + e
	}
	return u
}

func setupTest(t *testing.T) *sql.DB {
	t.Helper()
	DB = SetupTestDB(t)
	// Clear and re-init in-memory state
	State.mu.Lock()
	State.Peers = make(map[string]map[string]*Peer)
	State.Users = make(map[string]*UserUsage)
	State.Announces = 0
	State.Scrapes = 0
	State.mu.Unlock()

	// Disable passkey auth for standard tests
	SetTrackerSecret("")

	// Register the test hash to bypass Registry-Only check
	DB.Exec("INSERT INTO registry (info_hash, name, created_at) VALUES (?, ?, ?)", v2Hash, "Test Torrent", 0)
	return DB
}

func TestAnnounceValidRequest(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "peer001", "6881"), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()

	HandleAnnounce(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	if strings.Contains(body, "failure reason") {
		t.Errorf("Unexpected failure: %s", body)
	}
}

func TestAnnounceMissingParams(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	tests := []string{
		"/announce?peer_id=peer001&port=6881",                                 // missing info_hash
		"/announce?info_hash=" + url.QueryEscape(v2Hash) + "&port=6881",       // missing peer_id
		"/announce?info_hash=" + url.QueryEscape(v2Hash) + "&peer_id=peer001", // missing port
	}

	for _, u := range tests {
		req := httptest.NewRequest("GET", u, nil)
		req.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		HandleAnnounce(w, req)

		if w.Code != http.StatusOK {
			t.Errorf("Expected 200 for %s, got %d", u, w.Code)
		}
		body := w.Body.String()
		if !strings.Contains(body, "failure reason") {
			t.Errorf("Expected bencoded failure reason for %s, got: %s", u, body)
		}
	}
}

func TestAnnounceAcceptsV1Hash(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	// 20-byte hash (v1) represented as 40 hex chars
	v1HashHex := "1234567890123456789012345678901234567890"
	v2HashForV1 := "0000000000000000000000000000000000000000000000000000000000000001"
	DB.Exec("INSERT INTO registry (info_hash, v1_info_hash, name, created_at) VALUES (?, ?, ?, ?)",
		v2HashForV1, v1HashHex, "V1 Torrent", 0)

	req := httptest.NewRequest("GET", buildAnnounceURL(v1HashHex, "peer001", "6881"), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	body := w.Body.String()
	if strings.Contains(body, "failure reason") {
		t.Errorf("v1 hash should be accepted for hybrid support, got: %s", body)
	}
}

func TestAnnounceRejectsShortHash(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	// 4 bytes binary (8 hex chars)
	shortHashHex := "12345678"
	req := httptest.NewRequest("GET", buildAnnounceURL(shortHashHex, "peer001", "6881"), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "failure reason") {
		t.Errorf("Expected failure for short hash, got: %s", body)
	}
}

func TestAnnounceMultiplePeers(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	for i := 0; i < 3; i++ {
		req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "peer00"+string(rune('0'+i)), "688"+string(rune('0'+i))), nil)
		req.RemoteAddr = "127.0.0.1:5000"
		w := httptest.NewRecorder()
		HandleAnnounce(w, req)
	}

	req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "fetch", "9999"), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	if len(w.Body.Bytes()) == 0 {
		t.Error("Expected non-empty response")
	}
}

func TestAnnouncePeerUpdate(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	req1 := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "peer001", "6881"), nil)
	req1.RemoteAddr = "127.0.0.1:5000"
	w1 := httptest.NewRecorder()
	HandleAnnounce(w1, req1)

	req2 := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "peer001", "6882"), nil)
	req2.RemoteAddr = "127.0.0.1:5001"
	w2 := httptest.NewRecorder()
	HandleAnnounce(w2, req2)

	// Check in-memory state
	peers := State.GetPeers(v2Hash, "none", 10)
	if len(peers) != 1 {
		t.Errorf("Expected 1 peer, got %d", len(peers))
	}
}

func TestAnnounceWithStoppedEvent(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	State.UpdatePeer(v2Hash, "peer1", &Peer{Addr: "127.0.0.1:6881", UpdatedAt: time.Now().Unix()})

	req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "peer1", "6881", "event=stopped"), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	peers := State.GetPeers(v2Hash, "none", 10)
	if len(peers) != 0 {
		t.Errorf("Expected peer to be removed, found %d", len(peers))
	}
}

func TestAnnounceExcludesRequester(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	State.UpdatePeer(v2Hash, "peer1", &Peer{Addr: "127.0.0.1:6881", UpdatedAt: time.Now().Unix()})
	State.UpdatePeer(v2Hash, "peer2", &Peer{Addr: "127.0.0.1:6882", UpdatedAt: time.Now().Unix()})

	req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "peer1", "6881"), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	var resp map[string]interface{}
	if err := bencode.DecodeString(w.Body.String(), &resp); err != nil {
		t.Fatalf("Failed to decode bencode: %v", err)
	}
	peers, ok := resp["peers"].(string)
	if !ok {
		t.Fatal("Expected peers as string (compact format)")
	}
	if len(peers) != 6 {
		t.Errorf("Expected 6 bytes (1 compact peer), got %d", len(peers))
	}
}

func TestAnnounceTracksStats(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "seeder", "6881", "left=0", "downloaded=1000", "uploaded=500"), nil)
	req.RemoteAddr = "10.0.0.1:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	// Check memory state
	State.mu.RLock()
	peer := State.Peers[v2Hash]["seeder"]
	State.mu.RUnlock()
	if peer == nil || peer.Left != 0 || peer.Downloaded != 1000 || peer.Uploaded != 500 {
		t.Errorf("Stats not stored correctly: %+v", peer)
	}

	req2 := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "leecher", "6882", "left=5000"), nil)
	req2.RemoteAddr = "10.0.0.2:5000"
	w2 := httptest.NewRecorder()
	HandleAnnounce(w2, req2)

	var resp map[string]interface{}
	if err := bencode.DecodeString(w2.Body.String(), &resp); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	if complete, ok := resp["complete"].(int64); !ok || complete != 1 {
		t.Errorf("Expected complete=1, got %v", resp["complete"])
	}
	if incomplete, ok := resp["incomplete"].(int64); !ok || incomplete != 1 {
		t.Errorf("Expected incomplete=1, got %v", resp["incomplete"])
	}
}

func TestAnnounceNumwant(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	for i := 0; i < 5; i++ {
		State.UpdatePeer(v2Hash, string(rune('a'+i)), &Peer{Addr: "127.0.0.1:1000" + string(rune('1'+i)), UpdatedAt: time.Now().Unix()})
	}

	req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "requester", "9999", "numwant=2"), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	var resp map[string]interface{}
	if err := bencode.DecodeString(w.Body.String(), &resp); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	peers := resp["peers"].(string)
	if len(peers) > 12 {
		t.Errorf("Expected at most 2 peers (12 bytes), got %d bytes", len(peers))
	}
}

func TestAnnounceMaxPeersCap(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	oldMax := MaxPeers
	MaxPeers = 3
	defer func() { MaxPeers = oldMax }()

	for i := 0; i < 5; i++ {
		State.UpdatePeer(v2Hash, string(rune('a'+i)), &Peer{Addr: "127.0.0.1:1000" + string(rune('1'+i)), UpdatedAt: time.Now().Unix()})
	}

	req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "requester", "9999", "numwant=10"), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	var resp map[string]interface{}
	if err := bencode.DecodeString(w.Body.String(), &resp); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}
	peers := resp["peers"].(string)
	if len(peers) > 18 {
		t.Errorf("Expected at most 3 peers (18 bytes), got %d bytes", len(peers))
	}
}

func TestAnnounceCompactFormat(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	State.UpdatePeer(v2Hash, "peer1", &Peer{Addr: "192.168.1.1:6881", UpdatedAt: time.Now().Unix()})

	req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "other", "9999"), nil)
	req.RemoteAddr = "10.0.0.1:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	var resp map[string]interface{}
	if err := bencode.DecodeString(w.Body.String(), &resp); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	peers := resp["peers"].(string)
	if len(peers) != 6 {
		t.Fatalf("Expected 6 bytes, got %d", len(peers))
	}

	expected := []byte{0xC0, 0xA8, 0x01, 0x01, 0x1A, 0xE1}
	for i, b := range []byte(peers) {
		if b != expected[i] {
			t.Errorf("Byte %d: expected 0x%02X, got 0x%02X", i, expected[i], b)
		}
	}
}

func TestAnnounceIPv6(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	State.UpdatePeer(v2Hash, "peer6", &Peer{Addr: "[::1]:6881", UpdatedAt: time.Now().Unix()})

	req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "other", "9999"), nil)
	req.RemoteAddr = "10.0.0.1:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	var resp map[string]interface{}
	if err := bencode.DecodeString(w.Body.String(), &resp); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	peers6, ok := resp["peers6"].(string)
	if !ok {
		t.Fatal("Expected peers6 key for IPv6 peer")
	}
	if len(peers6) != 18 {
		t.Errorf("Expected 18 bytes for IPv6 compact peer, got %d", len(peers6))
	}
}

func TestAnnounceIPv6Client(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "v6client", "6881"), nil)
	req.RemoteAddr = "[::1]:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}

	peers := State.GetPeers(v2Hash, "none", 10)
	found := false
	for _, a := range peers {
		if a == "[::1]:6881" {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("Expected [::1]:6881 in state, not found")
	}
}

func TestAnnounceCompletedEvent(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	// Announce with event=completed
	req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "peer001", "6881", "event=completed", "left=0"), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}

	var completions int
	DB.QueryRow("SELECT completions FROM registry WHERE info_hash = ?", v2Hash).Scan(&completions)
	if completions != 1 {
		t.Errorf("Expected 1 completion, got %d", completions)
	}
}

func TestAnnounceBlockedHash(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	DB.Exec("INSERT INTO blocklist (info_hash, reason, created_at) VALUES (?, ?, ?)",
		v2Hash, "illegal content", 0)

	req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "peer001", "6881"), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	body := w.Body.String()
	if !strings.Contains(body, "blocked") {
		t.Errorf("Expected blocked failure, got: %s", body)
	}
}

func TestAnnounceUnregisteredHash(t *testing.T) {
	DB = SetupTestDB(t) // Note: NOT setupTest, so no hash registered
	defer DB.Close()

	// Ensure auth is disabled for this test
	SetTrackerSecret("")

	req := httptest.NewRequest("GET", buildAnnounceURL(v2Hash, "peer001", "6881"), nil)
	req.RemoteAddr = "127.0.0.1:5000"
	w := httptest.NewRecorder()
	HandleAnnounce(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "Unregistered torrent") {
		t.Errorf("Expected Unregistered failure reason, got %s", w.Body.String())
	}
}

func TestAnnounceWithPasskey(t *testing.T) {
	db := setupTest(t)
	defer db.Close()

	// 1. Enable auth
	secret := "test-secret"
	SetTrackerSecret(secret)
	defer SetTrackerSecret("")

	userID := "user123"
	signature := SignUserID(userID)
	passkey := userID + "." + signature

	// 2. Build URL with passkey in path
	u := buildAnnounceURL(v2Hash, "p1", "6881")
	u = "/announce/" + passkey + u[9:] // replace /announce with /announce/passkey

	req := httptest.NewRequest("GET", u, nil)
	req.RemoteAddr = "1.2.3.4:5000"
	w := httptest.NewRecorder()

	HandleAnnounce(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	if strings.Contains(w.Body.String(), "failure reason") {
		t.Errorf("Auth failed: %s", w.Body.String())
	}
}

func TestPackPeersIPv4(t *testing.T) {
	ipv4, ipv6 := PackPeers([]string{"192.168.1.1:6881"})
	if len(ipv4) != 6 {
		t.Errorf("Expected 6 bytes, got %d", len(ipv4))
	}
	if len(ipv6) != 0 {
		t.Errorf("Expected 0 IPv6 bytes, got %d", len(ipv6))
	}
}

func TestPackPeersIPv6(t *testing.T) {
	ipv4, ipv6 := PackPeers([]string{"[::1]:6881"})
	if len(ipv4) != 0 {
		t.Errorf("Expected 0 IPv4 bytes, got %d", len(ipv4))
	}
	if len(ipv6) != 18 {
		t.Errorf("Expected 18 bytes, got %d", len(ipv6))
	}
}

func TestPackPeersMixed(t *testing.T) {
	ipv4, ipv6 := PackPeers([]string{"10.0.0.1:8080", "[fe80::1]:9090", "invalid"})
	if len(ipv4) != 6 {
		t.Errorf("Expected 6 IPv4 bytes, got %d", len(ipv4))
	}
	if len(ipv6) != 18 {
		t.Errorf("Expected 18 IPv6 bytes, got %d", len(ipv6))
	}
}

func TestPackPeersInvalid(t *testing.T) {
	ipv4, ipv6 := PackPeers([]string{"not-an-addr", "127.0.0.1:notaport", ":1234"})
	if len(ipv4) != 0 {
		t.Errorf("Expected 0 IPv4 bytes, got %d", len(ipv4))
	}
	if len(ipv6) != 0 {
		t.Errorf("Expected 0 IPv6 bytes, got %d", len(ipv6))
	}
}

func TestPackPeersEmpty(t *testing.T) {
	ipv4, ipv6 := PackPeers(nil)
	if len(ipv4) != 0 || len(ipv6) != 0 {
		t.Error("Expected empty results for nil input")
	}
}

func TestTrackerError(t *testing.T) {
	w := httptest.NewRecorder()
	TrackerError(w, "test error message")

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := bencode.DecodeString(w.Body.String(), &resp); err != nil {
		t.Fatalf("Failed to decode: %v", err)
	}

	reason, ok := resp["failure reason"].(string)
	if !ok || reason != "test error message" {
		t.Errorf("Expected 'test error message', got %v", resp["failure reason"])
	}
}
