package tracker

import (
	"database/sql"
	"encoding/hex"
	"log"
	"net/http"
	"os"

	"github.com/zeebo/bencode"
)

// maxScrapeHashes bounds the per-request work: each info_hash costs DB
// queries, so extras beyond this cap are silently ignored (as other trackers do).
const maxScrapeHashes = 100

// HandleScrape implements BEP 48 scrape convention.
// Returns swarm stats (complete, downloaded, incomplete) for each requested info_hash.
func HandleScrape(w http.ResponseWriter, r *http.Request) {
	metricScrapes.Inc()
	hashesRaw := r.URL.Query()["info_hash"]
	if len(hashesRaw) > maxScrapeHashes {
		hashesRaw = hashesRaw[:maxScrapeHashes]
	}
	openTracker := os.Getenv("OPEN_TRACKER") == "true"

	files := make(map[string]interface{})
	for _, hashRaw := range hashesRaw {
		if len(hashRaw) != 20 && len(hashRaw) != 32 {
			continue
		}

		// Convert binary hash to hex string
		hash := hex.EncodeToString([]byte(hashRaw))

		// One registry lookup serves both the registered check and the
		// completions count. ErrNoRows means unregistered.
		var downloaded int
		err := DB.QueryRow("SELECT COALESCE(completions, 0) FROM registry WHERE info_hash = ? OR v1_info_hash = ?", hash, hash).Scan(&downloaded)
		if err != nil {
			if err != sql.ErrNoRows {
				log.Printf("Scrape registry query error: %v", err)
			}
			// Registry-Only Tracking (skip if OPEN_TRACKER=true)
			if !openTracker {
				continue
			}
			downloaded = 0
		}

		var blocked int
		_ = DB.QueryRow("SELECT 1 FROM blocklist WHERE info_hash = ?", hash).Scan(&blocked)
		if blocked == 1 {
			continue
		}

		// Fetch counts from memory
		complete, incomplete := State.GetCounts(hash)

		files[hashRaw] = map[string]interface{}{
			"complete":   complete,
			"downloaded": downloaded,
			"incomplete": incomplete,
		}
	}

	resp := map[string]interface{}{
		"files": files,
	}

	w.Header().Set("Content-Type", "text/plain")
	if err := bencode.NewEncoder(w).Encode(resp); err != nil {
		log.Printf("Error encoding scrape response: %v", err)
	}
}
