package tracker

import (
	"database/sql"
	"testing"
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
