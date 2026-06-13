package tracker

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

func TestStatePersistence(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	// 1. Update memory state
	hash := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	peerID := "peer1"
	p := &Peer{
		Addr:       "1.2.3.4:5678",
		UpdatedAt:  time.Now().Unix(),
		Left:       100,
		Downloaded: 50,
		Uploaded:   20,
	}
	State.UpdatePeer(hash, peerID, p)

	// 2. Flush to DB
	State.FlushToDB()

	// 3. Verify in DB
	var addr string
	var left int64
	err := DB.QueryRow("SELECT addr, left FROM peers WHERE info_hash = ? AND peer_id = ?", hash, peerID).Scan(&addr, &left)
	if err != nil {
		t.Fatalf("Peer not found in DB: %v", err)
	}
	if addr != p.Addr || left != p.Left {
		t.Errorf("DB data mismatch: got %s, %d", addr, left)
	}

	// 4. Clear memory and load from DB
	State.mu.Lock()
	State.Peers = make(map[string]map[string]*Peer)
	State.mu.Unlock()

	if err := State.LoadFromDB(); err != nil {
		t.Fatalf("LoadFromDB failed: %v", err)
	}

	// 5. Verify memory state
	peers := State.GetPeers(hash, "none", 10)
	if len(peers) != 1 || peers[0] != p.Addr {
		t.Errorf("Memory state not restored correctly: %v", peers)
	}
}

func TestFlushToDBRollbackOnError(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	// Seed a known peer into memory
	hash := "abc123"
	State.mu.Lock()
	State.Peers = make(map[string]map[string]*Peer)
	State.Peers[hash] = map[string]*Peer{
		"p1": {Addr: "1.2.3.4:6881", UpdatedAt: time.Now().Unix()},
	}
	State.mu.Unlock()

	// First flush should succeed
	State.FlushToDB()

	var count int
	DB.QueryRow("SELECT COUNT(*) FROM peers").Scan(&count)
	if count != 1 {
		t.Fatalf("Expected 1 peer after flush, got %d", count)
	}

	// Drop the peers table to force errors on next flush
	DB.Exec("DROP TABLE peers")

	// Add another peer to memory
	State.mu.Lock()
	State.Peers[hash]["p2"] = &Peer{Addr: "5.6.7.8:6881", UpdatedAt: time.Now().Unix()}
	State.mu.Unlock()

	// This flush should fail gracefully (not panic)
	State.FlushToDB()
}

func TestFlushToDBTransactionIntegrity(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	// Seed peers
	State.mu.Lock()
	State.Peers = make(map[string]map[string]*Peer)
	State.Peers["h1"] = map[string]*Peer{
		"p1": {Addr: "1.1.1.1:6881", UpdatedAt: time.Now().Unix(), Left: 100},
	}
	State.mu.Unlock()

	State.FlushToDB()

	// Verify the peer was written correctly
	var addr string
	var left int64
	err := DB.QueryRow("SELECT addr, left FROM peers WHERE info_hash = ? AND peer_id = ?", "h1", "p1").Scan(&addr, &left)
	if err != nil {
		t.Fatalf("Expected peer in DB: %v", err)
	}
	if addr != "1.1.1.1:6881" || left != 100 {
		t.Errorf("Unexpected values: addr=%s left=%d", addr, left)
	}
}

func TestStatePruneMemory(t *testing.T) {
	// Setup fresh state
	State.mu.Lock()
	State.Peers = make(map[string]map[string]*Peer)
	State.mu.Unlock()

	hash := "h1"
	now := time.Now().Unix()

	// Fresh peer
	State.UpdatePeer(hash, "fresh", &Peer{Addr: "1.1.1.1:1", UpdatedAt: now})
	// Stale peer (2 hours ago)
	State.UpdatePeer(hash, "stale", &Peer{Addr: "2.2.2.2:2", UpdatedAt: now - 7200})

	State.PruneMemory()

	peers := State.GetPeers(hash, "none", 10)
	if len(peers) != 1 || peers[0] != "1.1.1.1:1" {
		t.Errorf("Expected 1 fresh peer, got %v", peers)
	}
}

func TestStateGetPeersLimit(t *testing.T) {
	State.mu.Lock()
	State.Peers = make(map[string]map[string]*Peer)
	State.mu.Unlock()

	hash := "limithash"
	now := time.Now().Unix()
	for i := 0; i < 5; i++ {
		id := "p" + strconv.Itoa(i)
		State.UpdatePeer(hash, id, &Peer{Addr: id + ":1", UpdatedAt: now})
	}

	// limit 0 (numwant=0) must return no peers, not one.
	if got := State.GetPeers(hash, "none", 0); len(got) != 0 {
		t.Errorf("limit 0: expected 0 peers, got %d (%v)", len(got), got)
	}
	// A positive limit caps the result.
	if got := State.GetPeers(hash, "none", 3); len(got) != 3 {
		t.Errorf("limit 3: expected 3 peers, got %d", len(got))
	}
	// A limit above the swarm size returns all peers.
	if got := State.GetPeers(hash, "none", 10); len(got) != 5 {
		t.Errorf("limit 10: expected 5 peers, got %d", len(got))
	}
}

func TestMetricsHandler(t *testing.T) {
	req := httptest.NewRequest("GET", "/metrics", nil)
	w := httptest.NewRecorder()
	promhttp.Handler().ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	body := w.Body.String()
	for _, metric := range []string{
		"tracker_announces_total",
		"tracker_scrapes_total",
		"tracker_active_peers",
		"tracker_registered_torrents",
		"tracker_swarms_total",
		"go_goroutines",
	} {
		if !strings.Contains(body, metric) {
			t.Errorf("Metrics missing %s", metric)
		}
	}
}

func TestFlushUsersResilience(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	// 1. Setup local Hub server that fails initially
	fail := true
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if fail {
			http.Error(w, "Busy", http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	os.Setenv("HUB_URL", ts.URL)
	defer os.Unsetenv("HUB_URL")

	// 2. Add usage to RAM
	State.mu.Lock()
	State.Users = make(map[string]*UserUsage)
	State.Users["u1"] = &UserUsage{Uploaded: 1000}
	State.mu.Unlock()

	// 3. Flush (should fail and move to SQLite)
	State.FlushUsers()

	State.mu.RLock()
	if len(State.Users) != 0 {
		t.Error("Users should be cleared from RAM after move to SQLite")
	}
	s2 := State.Users["u1"]
	State.mu.RUnlock()
	if s2 != nil {
		t.Error("u1 should be gone from RAM")
	}

	var count int
	DB.QueryRow("SELECT COUNT(*) FROM usage_backlog").Scan(&count)
	if count != 1 {
		t.Errorf("Expected 1 record in backlog, got %d", count)
	}

	// 4. Hub comes back online
	fail = false

	// 5. Drain (should sync to Hub and clear SQLite)
	State.DrainBacklog()

	DB.QueryRow("SELECT COUNT(*) FROM usage_backlog").Scan(&count)
	if count != 0 {
		t.Error("Backlog should be empty after successful drain")
	}
}
