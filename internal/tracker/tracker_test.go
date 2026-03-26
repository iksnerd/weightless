package tracker

import (
	"database/sql"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

func SetupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	testDB, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("Failed to open test DB: %v", err)
	}

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
		if _, err := testDB.Exec(schema); err != nil {
			t.Fatalf("Failed to init schema: %v", err)
		}
	}
	return testDB
}

func TestHealthHandler(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	HealthHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	if w.Body.String() != "OK" {
		t.Errorf("Expected 'OK', got %s", w.Body.String())
	}
}

func TestHealthHandlerUnhealthy(t *testing.T) {
	testDB := SetupTestDB(t)
	testDB.Close()
	DB = testDB

	req := httptest.NewRequest("GET", "/health", nil)
	w := httptest.NewRecorder()
	HealthHandler(w, req)

	if w.Code != http.StatusServiceUnavailable {
		t.Errorf("Expected 503, got %d", w.Code)
	}
}

func TestIndexHandler(t *testing.T) {
	req := httptest.NewRequest("GET", "/", nil)
	w := httptest.NewRecorder()
	IndexHandler(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	if w.Body.String() != "Weightless Tracker v1.0" {
		t.Errorf("Unexpected response: %s", w.Body.String())
	}
}

func TestIndexHandlerNotFound(t *testing.T) {
	req := httptest.NewRequest("GET", "/invalid", nil)
	w := httptest.NewRecorder()
	IndexHandler(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected 404, got %d", w.Code)
	}
}

func TestMain(m *testing.M) {
	os.Exit(m.Run())
}
