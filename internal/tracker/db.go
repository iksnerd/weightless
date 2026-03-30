package tracker

import (
	"database/sql"
	"log"
	"os"
	"time"
)

var DB *sql.DB

func MustOpenDB() *sql.DB {
	dbPath := os.Getenv("DB_PATH")
	if dbPath == "" {
		dbPath = "./weightless.db"
	}

	d, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		log.Fatalf("Failed to open DB: %v", err)
	}
	DB = d
	return d
}

func InitSchema() {
	schemas := []string{
		`CREATE TABLE IF NOT EXISTS peers (
			info_hash TEXT,
			peer_id TEXT,
			addr TEXT,
			updated_at INTEGER,
			left INTEGER DEFAULT 0,
			downloaded INTEGER DEFAULT 0,
			uploaded INTEGER DEFAULT 0,
			PRIMARY KEY (info_hash, peer_id)
		)`,
		`CREATE INDEX IF NOT EXISTS idx_swarm_hash ON peers(info_hash)`,
		`CREATE TABLE IF NOT EXISTS blocklist (
			info_hash TEXT PRIMARY KEY,
			reason TEXT DEFAULT '',
			created_at INTEGER
		)`,
		`CREATE TABLE IF NOT EXISTS registry (
			info_hash TEXT PRIMARY KEY,
			v1_info_hash TEXT DEFAULT '',
			name TEXT,
			created_at INTEGER,
			verified BOOLEAN DEFAULT 0,
			completions INTEGER DEFAULT 0,
			description TEXT DEFAULT '',
			publisher TEXT DEFAULT '',
			license TEXT DEFAULT '',
			size INTEGER DEFAULT 0,
			category TEXT DEFAULT '',
			tags TEXT DEFAULT '',
			torrent_data BLOB
		)`,
		`CREATE TABLE IF NOT EXISTS usage_backlog (
			user_id TEXT,
			uploaded INTEGER DEFAULT 0,
			downloaded INTEGER DEFAULT 0,
			created_at INTEGER
		)`,
	}
	for _, schema := range schemas {
		if _, err := DB.Exec(schema); err != nil {
			log.Fatalf("Failed to initialize schema: %v", err)
		}
	}

	log.Println("Schema initialized")
}

func startPruner() {
	ticker := time.NewTicker(30 * time.Minute)
	for range ticker.C {
		if err := PrunePeers(); err != nil {
			log.Printf("Error pruning peers: %v", err)
		}
	}
}

var disablePrune bool

func MaybePrunePeers() {
	if disablePrune {
		return
	}
	// Probabilistically prune peers (1/100 requests)
	// This helps in serverless environments where a background pruner
	// may not run frequently.
	if time.Now().UnixNano()%100 == 0 {
		go func() {
			if err := PrunePeers(); err != nil {
				log.Printf("Probabilistic prune error: %v", err)
			}
		}()
	}
}

func PrunePeers() error {
	expiry := time.Now().Unix() - 3600
	res, err := DB.Exec("DELETE FROM peers WHERE updated_at < ?", expiry)
	if err != nil {
		return err
	}
	rows, _ := res.RowsAffected()
	if rows > 0 {
		log.Printf("Pruned %d stale peers", rows)
	}
	return nil
}
