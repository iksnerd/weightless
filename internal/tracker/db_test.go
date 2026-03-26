package tracker

import (
	"database/sql"
	"testing"
	"time"
)

func TestInitSchemaCreatesTablesAndIndex(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer testDB.Close()

	oldDB := DB
	DB = testDB
	defer func() { DB = oldDB }()

	InitSchema()

	// Verify tables
	for _, table := range []string{"peers", "registry"} {
		var name string
		err := testDB.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil || name != table {
			t.Errorf("Table %s not found", table)
		}
	}

	// Verify index
	var indexName string
	err = testDB.QueryRow("SELECT name FROM sqlite_master WHERE type='index' AND name='idx_swarm_hash'").Scan(&indexName)
	if err != nil || indexName != "idx_swarm_hash" {
		t.Errorf("Index idx_swarm_hash not found")
	}

	// Verify peer columns exist
	for _, col := range []string{"left", "downloaded", "uploaded"} {
		_, err := testDB.Exec("SELECT " + col + " FROM peers LIMIT 0")
		if err != nil {
			t.Errorf("Column %s not found in peers table: %v", col, err)
		}
	}

	// Verify registry columns exist
	for _, col := range []string{"completions", "description", "publisher", "license", "size", "category", "tags"} {
		_, err := testDB.Exec("SELECT " + col + " FROM registry LIMIT 0")
		if err != nil {
			t.Errorf("Column %s not found in registry table: %v", col, err)
		}
	}
}

func TestInitSchemaIdempotent(t *testing.T) {
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}
	defer testDB.Close()

	oldDB := DB
	DB = testDB
	defer func() { DB = oldDB }()

	// Call twice — should not panic or error
	InitSchema()
	InitSchema()
}

func TestPrunePeers(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	now := time.Now().Unix()
	DB.Exec(`INSERT INTO peers (info_hash, peer_id, addr, updated_at) VALUES (?, ?, ?, ?)`,
		"hash1", "peer1", "127.0.0.1:6881", now)

	staleTime := now - 3601
	DB.Exec(`INSERT INTO peers (info_hash, peer_id, addr, updated_at) VALUES (?, ?, ?, ?)`,
		"hash1", "peer2", "127.0.0.1:6882", staleTime)

	err := PrunePeers()
	if err != nil {
		t.Fatalf("Error pruning: %v", err)
	}

	var count int
	DB.QueryRow("SELECT COUNT(*) FROM peers WHERE peer_id = ?", "peer1").Scan(&count)
	if count != 1 {
		t.Error("Fresh peer should not be pruned")
	}

	DB.QueryRow("SELECT COUNT(*) FROM peers WHERE peer_id = ?", "peer2").Scan(&count)
	if count != 0 {
		t.Error("Stale peer should be pruned")
	}
}

func TestPrunePeersNothingToDelete(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	err := PrunePeers()
	if err != nil {
		t.Fatalf("Error pruning empty table: %v", err)
	}
}
