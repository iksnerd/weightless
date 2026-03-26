package tracker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

// Peer represents an active BitTorrent peer in memory.
type Peer struct {
	Addr       string
	UpdatedAt  int64
	Left       int64
	Downloaded int64
	Uploaded   int64
}

type UserUsage struct {
	Uploaded   int64
	Downloaded int64
}

// SwarmState manages the in-memory peer lists, metrics, and user usage sessions.
type SwarmState struct {
	mu    sync.RWMutex
	Peers map[string]map[string]*Peer // info_hash -> peer_id -> Peer
	Users map[string]*UserUsage       // user_id -> usage deltas

	// Metrics
	Announces  uint64
	Scrapes    uint64
	Registered uint64
}

var State = &SwarmState{
	Peers: make(map[string]map[string]*Peer),
	Users: make(map[string]*UserUsage),
}

// TrackUsage updates the in-memory usage deltas for a user.
func (s *SwarmState) TrackUsage(userID string, uploaded, downloaded int64) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Users[userID] == nil {
		s.Users[userID] = &UserUsage{}
	}
	s.Users[userID].Uploaded += uploaded
	s.Users[userID].Downloaded += downloaded
}

// GetPeer returns a single peer from memory.
func (s *SwarmState) GetPeer(hash, peerID string) *Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if swarm, ok := s.Peers[hash]; ok {
		return swarm[peerID]
	}
	return nil
}

// LoadFromDB populates the in-memory state from SQLite on startup.
func (s *SwarmState) LoadFromDB() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	rows, err := DB.Query("SELECT info_hash, peer_id, addr, updated_at, left, downloaded, uploaded FROM peers")
	if err != nil {
		return err
	}
	defer rows.Close()

	count := 0
	for rows.Next() {
		var hash, id, addr string
		var updated, left, down, up int64
		if err := rows.Scan(&hash, &id, &addr, &updated, &left, &down, &up); err != nil {
			continue
		}
		if s.Peers[hash] == nil {
			s.Peers[hash] = make(map[string]*Peer)
		}
		s.Peers[hash][id] = &Peer{
			Addr:       addr,
			UpdatedAt:  updated,
			Left:       left,
			Downloaded: down,
			Uploaded:   up,
		}
		count++
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("LoadFromDB row iteration: %w", err)
	}

	var regCount uint64
	_ = DB.QueryRow("SELECT COUNT(*) FROM registry").Scan(&regCount)
	atomic.StoreUint64(&s.Registered, regCount)

	log.Printf("Loaded %d peers and %d torrents into memory", count, s.Registered)
	return nil
}

// UpdatePeer adds or updates a peer in the memory map.
func (s *SwarmState) UpdatePeer(hash, id string, p *Peer) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Peers[hash] == nil {
		s.Peers[hash] = make(map[string]*Peer)
	}
	s.Peers[hash][id] = p
	atomic.AddUint64(&s.Announces, 1)
}

// RemovePeer removes a peer (e.g., on 'stopped' event).
func (s *SwarmState) RemovePeer(hash, id string) {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.Peers[hash] != nil {
		delete(s.Peers[hash], id)
	}
}

// GetPeers returns a list of peer addresses for a swarm, excluding the requester.
func (s *SwarmState) GetPeers(hash, excludeID string, limit int) []string {
	s.mu.RLock()
	defer s.mu.RUnlock()

	swarm := s.Peers[hash]
	if swarm == nil {
		return nil
	}

	var addrs []string
	for id, p := range swarm {
		if id == excludeID {
			continue
		}
		addrs = append(addrs, p.Addr)
		if len(addrs) >= limit {
			break
		}
	}
	return addrs
}

// GetCounts returns seeder and leecher counts for a swarm.
func (s *SwarmState) GetCounts(hash string) (complete, incomplete int) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	swarm := s.Peers[hash]
	for _, p := range swarm {
		if p.Left == 0 {
			complete++
		} else {
			incomplete++
		}
	}
	return
}

// FlushToDB persists the current memory state back to SQLite.
func (s *SwarmState) FlushToDB() {
	s.mu.RLock()
	// Take a snapshot of peers to minimize lock time
	snapshot := make(map[string]map[string]*Peer)
	for hash, swarm := range s.Peers {
		snapshot[hash] = make(map[string]*Peer)
		for id, p := range swarm {
			snapshot[hash][id] = p
		}
	}
	s.mu.RUnlock()

	tx, err := DB.Begin()
	if err != nil {
		log.Printf("Flush error (begin): %v", err)
		return
	}
	defer tx.Rollback()

	if _, err := tx.Exec("DELETE FROM peers"); err != nil {
		log.Printf("Flush error (delete): %v", err)
		return
	}
	stmt, err := tx.Prepare("INSERT INTO peers (info_hash, peer_id, addr, updated_at, left, downloaded, uploaded) VALUES (?, ?, ?, ?, ?, ?, ?)")
	if err != nil {
		log.Printf("Flush error (prepare): %v", err)
		return
	}
	defer stmt.Close()

	for hash, swarm := range snapshot {
		for id, p := range swarm {
			if _, err := stmt.Exec(hash, id, p.Addr, p.UpdatedAt, p.Left, p.Downloaded, p.Uploaded); err != nil {
				log.Printf("Flush error (insert peer %s/%s): %v", hash, id, err)
				return
			}
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Flush error (commit): %v", err)
	}
}

// FlushUsers sends in-memory usage deltas to the external Hub.
// It is resilient: if the Hub is down, it keeps the deltas in RAM to try again later.
func (s *SwarmState) FlushUsers() {
	s.mu.RLock()
	if len(s.Users) == 0 {
		s.mu.RUnlock()
		return
	}

	hubURL := os.Getenv("HUB_URL")
	if hubURL == "" {
		s.mu.RUnlock()
		log.Println("HUB_URL not set, usage deltas accumulating in RAM")
		return
	}

	// Prepare payload from snapshot
	payload := make(map[string]UserUsage)
	for id, usage := range s.Users {
		payload[id] = *usage
	}
	s.mu.RUnlock()

	// Real production-ready HTTP POST sync
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("Failed to marshal usage payload: %v", err)
		return
	}

	req, err := http.NewRequest("POST", hubURL+"/api/internal/usage-sync", bytes.NewBuffer(body))
	if err != nil {
		log.Printf("Failed to create sync request: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")

	// Secure the sync with the registry key
	if key := os.Getenv("REGISTRY_KEY"); key != "" {
		req.Header.Set("X-Weightless-Key", key)
	}

	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		log.Printf("Hub sync unreachable (%v), holding %d users in RAM", err, len(payload))
		return
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		log.Printf("Hub sync returned error %d, backing up to SQLite", resp.StatusCode)
		s.backupUsageToDB(payload)
	} else {
		log.Printf("Successfully synced usage for %d users to Hub", len(payload))
	}

	s.mu.Lock()
	// Clear ONLY the users we just sent (to avoid clearing new data added during the POST)
	for id := range payload {
		delete(s.Users, id)
	}
	s.mu.Unlock()
}

// backupUsageToDB saves usage deltas to the local SQLite backlog for later draining.
func (s *SwarmState) backupUsageToDB(usage map[string]UserUsage) {
	tx, err := DB.Begin()
	if err != nil {
		log.Printf("Failed to start backlog tx: %v", err)
		return
	}
	defer tx.Rollback()

	stmt, err := tx.Prepare("INSERT INTO usage_backlog (user_id, uploaded, downloaded, created_at) VALUES (?, ?, ?, ?)")
	if err != nil {
		log.Printf("Failed to prepare backlog stmt: %v", err)
		return
	}
	defer stmt.Close()

	now := time.Now().Unix()
	for id, u := range usage {
		if _, err := stmt.Exec(id, u.Uploaded, u.Downloaded, now); err != nil {
			log.Printf("Failed to exec backlog insert: %v", err)
		}
	}

	if err := tx.Commit(); err != nil {
		log.Printf("Failed to commit usage backlog: %v", err)
	}
}

// DrainBacklog attempts to sync the SQLite usage backlog to the external Hub.
func (s *SwarmState) DrainBacklog() {
	hubURL := os.Getenv("HUB_URL")
	if hubURL == "" {
		return
	}

	// 1. Fetch oldest backlog records
	rows, err := DB.Query("SELECT rowid, user_id, uploaded, downloaded FROM usage_backlog ORDER BY created_at ASC LIMIT 100")
	if err != nil {
		return
	}
	defer rows.Close()

	type record struct {
		rowid int64
		id    string
		u     UserUsage
	}
	var records []record
	payload := make(map[string]UserUsage)

	for rows.Next() {
		var r record
		if err := rows.Scan(&r.rowid, &r.id, &r.u.Uploaded, &r.u.Downloaded); err != nil {
			continue
		}
		records = append(records, r)
		// Accumulate if same user appears multiple times in backlog
		existing := payload[r.id]
		existing.Uploaded += r.u.Uploaded
		existing.Downloaded += r.u.Downloaded
		payload[r.id] = existing
	}

	if len(records) == 0 {
		return
	}

	// 2. Try to sync to Hub
	body, err := json.Marshal(payload)
	if err != nil {
		log.Printf("DrainBacklog: marshal error: %v", err)
		return
	}
	req, err := http.NewRequest("POST", hubURL+"/api/internal/usage-sync", bytes.NewBuffer(body))
	if err != nil {
		log.Printf("DrainBacklog: request creation error: %v", err)
		return
	}
	req.Header.Set("Content-Type", "application/json")
	if key := os.Getenv("REGISTRY_KEY"); key != "" {
		req.Header.Set("X-Weightless-Key", key)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return // network error, try again next cycle
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return // non-200, try again next cycle
	}

	// 3. Delete synced records from DB
	for _, r := range records {
		_, _ = DB.Exec("DELETE FROM usage_backlog WHERE rowid = ?", r.rowid)
	}
	log.Printf("Drained %d records from usage backlog to Hub", len(records))
}

// PruneMemory removes peers from RAM that haven't announced in over an hour.
func (s *SwarmState) PruneMemory() {
	s.mu.Lock()
	defer s.mu.Unlock()

	expiry := time.Now().Unix() - 3600
	count := 0
	for hash, swarm := range s.Peers {
		for id, p := range swarm {
			if p.UpdatedAt < expiry {
				delete(swarm, id)
				count++
			}
		}
		if len(swarm) == 0 {
			delete(s.Peers, hash)
		}
	}
	if count > 0 {
		log.Printf("Pruned %d stale peers from memory", count)
	}
}

// MetricsHandler exposes internal state in Prometheus format.
func (s *SwarmState) MetricsHandler(w http.ResponseWriter, r *http.Request) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	totalPeers := 0
	for _, swarm := range s.Peers {
		totalPeers += len(swarm)
	}

	fmt.Fprintf(w, "# HELP tracker_announces_total Total number of announce requests handled\n")
	fmt.Fprintf(w, "tracker_announces_total %d\n", atomic.LoadUint64(&s.Announces))
	fmt.Fprintf(w, "# HELP tracker_scrapes_total Total number of scrape requests handled\n")
	fmt.Fprintf(w, "tracker_scrapes_total %d\n", atomic.LoadUint64(&s.Scrapes))
	fmt.Fprintf(w, "# HELP tracker_active_peers Current number of active peers in memory\n")
	fmt.Fprintf(w, "tracker_active_peers %d\n", totalPeers)
	fmt.Fprintf(w, "# HELP tracker_registered_torrents Total number of torrents in registry\n")
	fmt.Fprintf(w, "tracker_registered_torrents %d\n", atomic.LoadUint64(&s.Registered))
	fmt.Fprintf(w, "# HELP tracker_swarms_total Total number of active swarms\n")
	fmt.Fprintf(w, "tracker_swarms_total %d\n", len(s.Peers))
}
