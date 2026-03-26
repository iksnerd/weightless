package tracker

import (
	"encoding/hex"
	"log"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/zeebo/bencode"
)

// TrackerError writes a BEP 3 bencoded failure response (always HTTP 200).
func TrackerError(w http.ResponseWriter, reason string) {
	w.Header().Set("Content-Type", "text/plain")
	if err := bencode.NewEncoder(w).Encode(map[string]interface{}{
		"failure reason": reason,
	}); err != nil {
		log.Printf("Error encoding weightless error: %v", err)
	}
}

func HandleAnnounce(w http.ResponseWriter, r *http.Request) {
	// In serverless environments, background goroutines might not run.
	// We run a probabilistic prune on incoming requests.
	MaybePrunePeers()

	// 1. Extract and Verify Passkey (Path-based)
	// Expects /announce/USER_ID.SIGNATURE
	pathParts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var userID string
	if len(pathParts) >= 2 && pathParts[0] == "announce" {
		var err error
		userID, err = VerifyPasskey(pathParts[1])
		if err != nil {
			TrackerError(w, "Unauthorized: "+err.Error())
			return
		}
	} else if os.Getenv("TRACKER_SECRET") != "" {
		// If secret is set, we ENFORCE signed passkeys
		TrackerError(w, "Unauthorized: Passkey required in path (/announce/ID.SIG)")
		return
	}

	q := r.URL.Query()
	hashRaw := q.Get("info_hash")
	peerID := q.Get("peer_id")
	port := q.Get("port")
	event := q.Get("event")

	if hashRaw == "" || peerID == "" || port == "" {
		TrackerError(w, "Missing required parameters (info_hash, peer_id, port)")
		return
	}

	// Accept both v1 (20-byte SHA-1) and v2 (32-byte SHA-256) info hashes
	// to support hybrid torrents where clients may announce with either hash.
	if len(hashRaw) != 20 && len(hashRaw) != 32 {
		TrackerError(w, "Invalid info_hash: must be 20 bytes (v1) or 32 bytes (v2)")
		return
	}

	// Convert binary hash to hex string for database lookups
	hash := hex.EncodeToString([]byte(hashRaw))

	// Check blocklist
	var blocked int
	_ = DB.QueryRow("SELECT 1 FROM blocklist WHERE info_hash = ?", hash).Scan(&blocked)
	if blocked == 1 {
		TrackerError(w, "info_hash is blocked")
		return
	}

	// Check registry (Registry-Only Tracking)
	// We only track swarms for torrents that have been officially registered.
	// We check both v2 (info_hash) and v1 (v1_info_hash) to support hybrid torrents.
	var registered int
	err := DB.QueryRow("SELECT 1 FROM registry WHERE info_hash = ? OR v1_info_hash = ?", hash, hash).Scan(&registered)
	if err != nil || registered == 0 {
		TrackerError(w, "Unregistered torrent: info_hash not found in weightless registry")
		return
	}

	// Extract clean IP from RemoteAddr (which is already IP:port)
	clientIP, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		TrackerError(w, "Invalid remote address")
		return
	}
	addr := net.JoinHostPort(clientIP, port)

	// Parse stats (BEP 3)
	left, _ := strconv.ParseInt(q.Get("left"), 10, 64)
	downloaded, _ := strconv.ParseInt(q.Get("downloaded"), 10, 64)
	uploaded, _ := strconv.ParseInt(q.Get("uploaded"), 10, 64)

	if event == "stopped" {
		State.RemovePeer(hash, peerID)
	} else {
		// Calculate session delta for seeder economy
		if userID != "" {
			oldPeer := State.GetPeer(hash, peerID)
			if oldPeer != nil {
				deltaUp := uploaded - oldPeer.Uploaded
				deltaDown := downloaded - oldPeer.Downloaded
				// Sanity check: prevent negative deltas if client resets counters
				if deltaUp > 0 || deltaDown > 0 {
					State.TrackUsage(userID, deltaUp, deltaDown)
				}
			}
		}

		State.UpdatePeer(hash, peerID, &Peer{
			Addr:       addr,
			UpdatedAt:  time.Now().Unix(),
			Left:       left,
			Downloaded: downloaded,
			Uploaded:   uploaded,
		})

		// Track completions for scrape (fire-and-forget)
		if event == "completed" {
			if _, err := DB.Exec("UPDATE registry SET completions = completions + 1 WHERE info_hash = ?", hash); err != nil {
				log.Printf("Error updating completions: %v", err)
			}
		}
	}

	// Determine peer limit: min(numwant, MaxPeers)
	limit := MaxPeers
	if nw := q.Get("numwant"); nw != "" {
		if n, err := strconv.Atoi(nw); err == nil && n > 0 && n < limit {
			limit = n
		}
	}

	// Fetch swarm from memory (excluding requester)
	addrs := State.GetPeers(hash, peerID, limit)

	// Seeder/leecher counts from memory
	complete, incomplete := State.GetCounts(hash)

	// Compact peer format (BEP 3 + BEP 7)
	ipv4, ipv6 := PackPeers(addrs)
	resp := map[string]interface{}{
		"interval":   1800,
		"complete":   complete,
		"incomplete": incomplete,
		"peers":      string(ipv4),
	}
	if len(ipv6) > 0 {
		resp["peers6"] = string(ipv6)
	}

	w.Header().Set("Content-Type", "text/plain")
	if err := bencode.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("Error encoding bencode response: %v", err)
	}
}

// PackPeers converts addr strings to compact binary format.
// IPv4: 6 bytes each (4 IP + 2 port big-endian) per BEP 3.
// IPv6: 18 bytes each (16 IP + 2 port big-endian) per BEP 7.
func PackPeers(addrs []string) (ipv4 []byte, ipv6 []byte) {
	for _, addr := range addrs {
		host, portStr, err := net.SplitHostPort(addr)
		if err != nil {
			continue
		}
		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}
		var portBytes [2]byte
		portBytes[0] = byte(port >> 8)
		portBytes[1] = byte(port & 0xff)

		ip := net.ParseIP(host)
		if ip4 := ip.To4(); ip4 != nil {
			ipv4 = append(ipv4, ip4...)
			ipv4 = append(ipv4, portBytes[:]...)
		} else if ip16 := ip.To16(); ip16 != nil {
			ipv6 = append(ipv6, ip16...)
			ipv6 = append(ipv6, portBytes[:]...)
		}
	}
	return
}
