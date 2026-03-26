package tracker

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"sort"
	"strconv"
	"sync/atomic"
	"time"

	"weightless/internal/torrent"
)

var registryKey = os.Getenv("REGISTRY_KEY")

type registryEntry struct {
	InfoHash    string `json:"info_hash"`
	V1InfoHash  string `json:"v1_info_hash,omitempty"`
	Name        string `json:"name"`
	Verified    bool   `json:"verified"`
	Completions int    `json:"completions"`
	Description string `json:"description,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	License     string `json:"license,omitempty"`
	Size        int64  `json:"size,omitempty"`
	Category    string `json:"category,omitempty"`
	Tags        string `json:"tags,omitempty"`
	Seeders     int    `json:"seeders"`
	Leechers    int    `json:"leechers"`
}

// fillSwarmStats populates seeders/leechers from the memory state.
func fillSwarmStats(entries []registryEntry) {
	for i := range entries {
		// 1. Get counts for the primary v2 info_hash (hex)
		s, l := State.GetCounts(entries[i].InfoHash)
		entries[i].Seeders = s
		entries[i].Leechers = l

		// 2. Add counts for the v1 info_hash (hex) if it exists and is different
		if entries[i].V1InfoHash != "" && entries[i].V1InfoHash != entries[i].InfoHash {
			s, l := State.GetCounts(entries[i].V1InfoHash)
			entries[i].Seeders += s
			entries[i].Leechers += l
		}
	}
}

var registryCols = `info_hash, v1_info_hash, name, verified, completions, description, publisher, license, size, category, tags`

func scanRegistryEntry(scanner interface{ Scan(...interface{}) error }) (registryEntry, error) {
	var e registryEntry
	err := scanner.Scan(&e.InfoHash, &e.V1InfoHash, &e.Name, &e.Verified, &e.Completions,
		&e.Description, &e.Publisher, &e.License, &e.Size, &e.Category, &e.Tags)
	return e, err
}

func HandleAPI(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	switch r.Method {
	case http.MethodGet:
		hash := r.URL.Query().Get("info_hash")
		if hash == "" {
			http.Error(w, "Missing info_hash", http.StatusBadRequest)
			return
		}

		e, err := scanRegistryEntry(DB.QueryRow("SELECT "+registryCols+" FROM registry WHERE info_hash = ?", hash))
		if errors.Is(err, sql.ErrNoRows) {
			http.Error(w, "Not found", http.StatusNotFound)
			return
		}
		if err != nil {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		entries := []registryEntry{e}
		fillSwarmStats(entries)

		if err := json.NewEncoder(w).Encode(entries[0]); err != nil {
			log.Printf("Error writing API response: %v", err)
		}

	case http.MethodPost:
		if registryKey != "" {
			if r.Header.Get("X-Weightless-Key") != registryKey {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}

		var body struct {
			InfoHash    string `json:"info_hash"`
			V1InfoHash  string `json:"v1_info_hash"`
			Name        string `json:"name"`
			Description string `json:"description"`
			Publisher   string `json:"publisher"`
			License     string `json:"license"`
			Size        int64  `json:"size"`
			Category    string `json:"category"`
			Tags        string `json:"tags"`
			TorrentData []byte `json:"torrent_data"`
		}
		r.Body = http.MaxBytesReader(w, r.Body, 100<<20) // 100MB for torrent data
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON body", http.StatusBadRequest)
			return
		}
		if body.InfoHash == "" || body.Name == "" {
			http.Error(w, "Missing info_hash or name", http.StatusBadRequest)
			return
		}

		_, err := DB.Exec(
			`INSERT INTO registry (info_hash, v1_info_hash, name, created_at, verified, description, publisher, license, size, category, tags, torrent_data)
			VALUES (?, ?, ?, ?, 0, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(info_hash)
			DO UPDATE SET name=excluded.name, v1_info_hash=excluded.v1_info_hash, description=excluded.description,
				publisher=excluded.publisher, license=excluded.license,
				size=excluded.size, category=excluded.category, tags=excluded.tags,
				torrent_data=excluded.torrent_data`,
			body.InfoHash, body.V1InfoHash, body.Name, time.Now().Unix(),
			body.Description, body.Publisher, body.License, body.Size, body.Category, body.Tags, body.TorrentData)
		if err != nil {
			http.Error(w, "Database error", http.StatusInternalServerError)
			return
		}

		// Update registered count in memory
		var regCount uint64
		_ = DB.QueryRow("SELECT COUNT(*) FROM registry").Scan(&regCount)
		atomic.StoreUint64(&State.Registered, regCount)

		w.WriteHeader(http.StatusCreated)
		if err := json.NewEncoder(w).Encode(map[string]string{"status": "created"}); err != nil {
			log.Printf("Error writing API response: %v", err)
		}

	case http.MethodDelete:
		if registryKey != "" {
			if r.Header.Get("X-Weightless-Key") != registryKey {
				http.Error(w, "Unauthorized", http.StatusUnauthorized)
				return
			}
		}

		hash := r.URL.Query().Get("info_hash")
		if hash == "" {
			http.Error(w, "Missing info_hash", http.StatusBadRequest)
			return
		}

		reason := r.URL.Query().Get("reason")

		if _, err := DB.Exec("DELETE FROM registry WHERE info_hash = ?", hash); err != nil {
			log.Printf("Registry delete error: %v", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		if _, err := DB.Exec("DELETE FROM peers WHERE info_hash = ?", hash); err != nil {
			log.Printf("Peer delete error: %v", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}
		State.mu.Lock()
		delete(State.Peers, hash)
		State.mu.Unlock()
		if _, err := DB.Exec("INSERT OR IGNORE INTO blocklist (info_hash, reason, created_at) VALUES (?, ?, ?)",
			hash, reason, time.Now().Unix()); err != nil {
			log.Printf("Blocklist insert error: %v", err)
			http.Error(w, "Internal error", http.StatusInternalServerError)
			return
		}

		// Update registered count in memory
		var regCount uint64
		if err := DB.QueryRow("SELECT COUNT(*) FROM registry").Scan(&regCount); err != nil {
			log.Printf("Registry count error: %v", err)
		} else {
			atomic.StoreUint64(&State.Registered, regCount)
		}

		if err := json.NewEncoder(w).Encode(map[string]string{"status": "deleted"}); err != nil {
			log.Printf("Error writing API response: %v", err)
		}

	default:
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
	}
}

func HandleTorrentDownload(w http.ResponseWriter, r *http.Request) {
	hash := r.URL.Query().Get("info_hash")
	if hash == "" {
		http.Error(w, "Missing info_hash", http.StatusBadRequest)
		return
	}

	var data []byte
	var name string
	err := DB.QueryRow("SELECT torrent_data, name FROM registry WHERE info_hash = ?", hash).Scan(&data, &name)
	if err != nil || len(data) == 0 {
		http.Error(w, "Torrent not found", http.StatusNotFound)
		return
	}

	w.Header().Set("Content-Type", "application/x-bittorrent")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.torrent"`, name))
	w.Write(data)
}

func HandleSearch(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	q := r.URL.Query()

	// Filtering
	where := " WHERE 1=1"
	var args []interface{}

	if v := q.Get("q"); v != "" {
		where += " AND name LIKE ?"
		args = append(args, "%"+v+"%")
	}
	if v := q.Get("category"); v != "" {
		where += " AND category = ?"
		args = append(args, v)
	}
	if v := q.Get("publisher"); v != "" {
		where += " AND publisher = ?"
		args = append(args, v)
	}
	if v := q.Get("tags"); v != "" {
		where += " AND tags LIKE ?"
		args = append(args, "%"+v+"%")
	}

	// Pagination
	limit := 50
	if v := q.Get("limit"); v != "" {
		if l, err := strconv.Atoi(v); err == nil && l > 0 {
			limit = l
			if limit > 100 {
				limit = 100
			}
		}
	}
	offset := 0
	if v := q.Get("offset"); v != "" {
		if o, err := strconv.Atoi(v); err == nil && o >= 0 {
			offset = o
		}
	}

	// Sorting — seeders is memory-only, so DB sorts by created_at and we re-sort after
	sortCol := "created_at"
	switch q.Get("sort") {
	case "completions":
		sortCol = "completions"
	case "seeders":
		sortCol = "created_at" // DB fallback; post-query sort below
	}

	// Get total count
	var total int
	err := DB.QueryRow("SELECT COUNT(*) FROM registry"+where, args...).Scan(&total)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	w.Header().Set("X-Total-Count", strconv.Itoa(total))

	// Main query
	query := "SELECT " + registryCols + " FROM registry" + where
	query += fmt.Sprintf(" ORDER BY %s DESC LIMIT %d OFFSET %d", sortCol, limit, offset)

	rows, err := DB.Query(query, args...)
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}
	defer rows.Close()

	var results []registryEntry
	for rows.Next() {
		e, err := scanRegistryEntry(rows)
		if err != nil {
			log.Printf("Error scanning registry row: %v", err)
			continue
		}
		results = append(results, e)
	}

	if results == nil {
		results = []registryEntry{}
	}

	fillSwarmStats(results)

	// Seeders are memory-only (not in SQLite), so sort post-query
	if q.Get("sort") == "seeders" {
		sort.Slice(results, func(i, j int) bool {
			return results[i].Seeders > results[j].Seeders
		})
	}

	if err := json.NewEncoder(w).Encode(results); err != nil {
		log.Printf("Error writing search response: %v", err)
	}
}

func HandleMetadata(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	hash := r.URL.Query().Get("info_hash")
	if hash == "" {
		http.Error(w, "Missing info_hash", http.StatusBadRequest)
		return
	}

	var data []byte
	err := DB.QueryRow("SELECT torrent_data FROM registry WHERE info_hash = ?", hash).Scan(&data)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "Not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, "Database error", http.StatusInternalServerError)
		return
	}

	if len(data) == 0 {
		http.Error(w, "No torrent data available", http.StatusNotFound)
		return
	}

	meta, err := torrent.Parse(data)
	if err != nil {
		http.Error(w, "Error parsing torrent data", http.StatusInternalServerError)
		return
	}

	if err := json.NewEncoder(w).Encode(meta); err != nil {
		log.Printf("Error writing metadata response: %v", err)
	}
}
