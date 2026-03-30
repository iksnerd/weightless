package tracker

import (
	"encoding/hex"
	"log"
	"net/http"
	"os"
	"sync/atomic"

	"github.com/zeebo/bencode"
)

// HandleScrape implements BEP 48 scrape convention.
// Returns swarm stats (complete, downloaded, incomplete) for each requested info_hash.
func HandleScrape(w http.ResponseWriter, r *http.Request) {
	atomic.AddUint64(&State.Scrapes, 1)
	hashesRaw := r.URL.Query()["info_hash"]

	files := make(map[string]interface{})
	for _, hashRaw := range hashesRaw {
		if len(hashRaw) != 20 && len(hashRaw) != 32 {
			continue
		}

		// Convert binary hash to hex string
		hash := hex.EncodeToString([]byte(hashRaw))

		// Registry-Only Tracking (skip if OPEN_TRACKER=true)
		if os.Getenv("OPEN_TRACKER") != "true" {
			var registered int
			err := DB.QueryRow("SELECT 1 FROM registry WHERE info_hash = ? OR v1_info_hash = ?", hash, hash).Scan(&registered)
			if err != nil || registered == 0 {
				continue
			}
		}

		var blocked int
		_ = DB.QueryRow("SELECT 1 FROM blocklist WHERE info_hash = ?", hash).Scan(&blocked)
		if blocked == 1 {
			continue
		}

		// Fetch counts from memory
		complete, incomplete := State.GetCounts(hash)

		var downloaded int
		_ = DB.QueryRow("SELECT COALESCE(completions, 0) FROM registry WHERE info_hash = ? OR v1_info_hash = ?", hash, hash).Scan(&downloaded)

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
