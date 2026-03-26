package tracker

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRegistryPost(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	body := `{"info_hash":"hash1","name":"TestFile"}`
	req := httptest.NewRequest("POST", "/api/registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected 201, got %d", w.Code)
	}

	var name string
	err := DB.QueryRow("SELECT name FROM registry WHERE info_hash = ?", "hash1").Scan(&name)
	if err != nil || name != "TestFile" {
		t.Errorf("Registry entry not created: %v", err)
	}
}

func TestRegistryPostWithAllFields(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	body := `{"info_hash":"hash2","name":"FullEntry","description":"A test dataset","publisher":"testlab","license":"MIT","size":1048576,"category":"models","tags":"llm,weights"}`
	req := httptest.NewRequest("POST", "/api/registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected 201, got %d", w.Code)
	}

	var desc, publisher, license, category, tags string
	var size int64
	DB.QueryRow("SELECT description, publisher, license, size, category, tags FROM registry WHERE info_hash = ?", "hash2").
		Scan(&desc, &publisher, &license, &size, &category, &tags)

	if desc != "A test dataset" {
		t.Errorf("Expected description 'A test dataset', got %q", desc)
	}
	if publisher != "testlab" {
		t.Errorf("Expected publisher 'testlab', got %q", publisher)
	}
	if license != "MIT" {
		t.Errorf("Expected license 'MIT', got %q", license)
	}
	if size != 1048576 {
		t.Errorf("Expected size 1048576, got %d", size)
	}
	if category != "models" {
		t.Errorf("Expected category 'models', got %q", category)
	}
	if tags != "llm,weights" {
		t.Errorf("Expected tags 'llm,weights', got %q", tags)
	}
}

func TestRegistryGet(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	DB.Exec("INSERT INTO registry (info_hash, name, created_at, verified) VALUES (?, ?, ?, 0)", "hash1", "TestFile", 0)

	req := httptest.NewRequest("GET", "/api/registry?info_hash=hash1", nil)
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}

	var entry registryEntry
	if err := json.NewDecoder(w.Body).Decode(&entry); err != nil {
		t.Fatalf("Decode: %v", err)
	}
	if entry.InfoHash != "hash1" || entry.Name != "TestFile" || entry.Verified != false {
		t.Errorf("Unexpected entry: %+v", entry)
	}
}

func TestRegistryGetReturnsAllFields(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	DB.Exec(`INSERT INTO registry (info_hash, name, created_at, verified, completions, description, publisher, license, size, category, tags)
		VALUES (?, ?, ?, 1, 10, 'desc', 'pub', 'MIT', 999, 'cat', 'tag1,tag2')`, "hash1", "Full", 0)

	req := httptest.NewRequest("GET", "/api/registry?info_hash=hash1", nil)
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	var entry registryEntry
	json.NewDecoder(w.Body).Decode(&entry)

	if entry.Completions != 10 {
		t.Errorf("Expected completions=10, got %d", entry.Completions)
	}
	if entry.Description != "desc" {
		t.Errorf("Expected description 'desc', got %q", entry.Description)
	}
	if entry.Category != "cat" {
		t.Errorf("Expected category 'cat', got %q", entry.Category)
	}
}

func TestRegistryGetNotFound(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	req := httptest.NewRequest("GET", "/api/registry?info_hash=nonexistent", nil)
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("Expected 404, got %d", w.Code)
	}
}

func TestRegistryPostMissingParams(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	tests := []struct {
		name string
		body string
	}{
		{"missing info_hash", `{"name":"test"}`},
		{"missing name", `{"info_hash":"abc"}`},
		{"empty body", `{}`},
	}

	for _, tt := range tests {
		req := httptest.NewRequest("POST", "/api/registry", strings.NewReader(tt.body))
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		HandleAPI(w, req)

		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: Expected 400, got %d", tt.name, w.Code)
		}
	}
}

func TestRegistryPostInvalidJSON(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	req := httptest.NewRequest("POST", "/api/registry", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", w.Code)
	}
}

func TestRegistryGetMissingParam(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	req := httptest.NewRequest("GET", "/api/registry", nil)
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", w.Code)
	}
}

func TestRegistryMethodNotAllowed(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	req := httptest.NewRequest("PUT", "/api/registry?info_hash=abc", nil)
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("Expected 405, got %d", w.Code)
	}
}

func TestRegistryUpdate(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	DB.Exec("INSERT INTO registry (info_hash, name, created_at, verified) VALUES (?, ?, ?, 0)", "hash1", "OldName", 0)

	body := `{"info_hash":"hash1","name":"NewName"}`
	req := httptest.NewRequest("POST", "/api/registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected 201, got %d", w.Code)
	}

	var name string
	DB.QueryRow("SELECT name FROM registry WHERE info_hash = ?", "hash1").Scan(&name)
	if name != "NewName" {
		t.Errorf("Expected NewName, got %s", name)
	}
}

func TestRegistryAPIContentType(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	DB.Exec("INSERT INTO registry (info_hash, name, created_at, verified) VALUES (?, ?, ?, 0)", "hash1", "Test", 0)

	req := httptest.NewRequest("GET", "/api/registry?info_hash=hash1", nil)
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Header().Get("Content-Type") != "application/json" {
		t.Errorf("Expected application/json, got %s", w.Header().Get("Content-Type"))
	}
}

func TestRegistryCompleteFlow(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	body1 := `{"info_hash":"abc123","name":"MyDocument"}`
	req1 := httptest.NewRequest("POST", "/api/registry", strings.NewReader(body1))
	req1.Header.Set("Content-Type", "application/json")
	w1 := httptest.NewRecorder()
	HandleAPI(w1, req1)
	if w1.Code != http.StatusCreated {
		t.Errorf("Expected 201, got %d", w1.Code)
	}

	req2 := httptest.NewRequest("GET", "/api/registry?info_hash=abc123", nil)
	w2 := httptest.NewRecorder()
	HandleAPI(w2, req2)
	if w2.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w2.Code)
	}

	body3 := `{"info_hash":"abc123","name":"MyDocumentV2"}`
	req3 := httptest.NewRequest("POST", "/api/registry", strings.NewReader(body3))
	req3.Header.Set("Content-Type", "application/json")
	w3 := httptest.NewRecorder()
	HandleAPI(w3, req3)
	if w3.Code != http.StatusCreated {
		t.Errorf("Expected 201, got %d", w3.Code)
	}

	var name string
	DB.QueryRow("SELECT name FROM registry WHERE info_hash = ?", "abc123").Scan(&name)
	if name != "MyDocumentV2" {
		t.Errorf("Expected MyDocumentV2, got %s", name)
	}
}

// --- API Key Auth Tests ---

func TestRegistryAuthRejectsWithoutKey(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	old := registryKey
	registryKey = "secret123"
	defer func() { registryKey = old }()

	body := `{"info_hash":"h1","name":"Test"}`
	req := httptest.NewRequest("POST", "/api/registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401, got %d", w.Code)
	}
}

func TestRegistryAuthRejectsWrongKey(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	old := registryKey
	registryKey = "secret123"
	defer func() { registryKey = old }()

	body := `{"info_hash":"h1","name":"Test"}`
	req := httptest.NewRequest("POST", "/api/registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Weightless-Key", "wrongkey")
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401, got %d", w.Code)
	}
}

func TestRegistryAuthAcceptsCorrectKey(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	old := registryKey
	registryKey = "secret123"
	defer func() { registryKey = old }()

	body := `{"info_hash":"h1","name":"Test"}`
	req := httptest.NewRequest("POST", "/api/registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Weightless-Key", "secret123")
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected 201, got %d", w.Code)
	}
}

func TestRegistryAuthNotRequiredWhenUnset(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	old := registryKey
	registryKey = ""
	defer func() { registryKey = old }()

	body := `{"info_hash":"h1","name":"Test"}`
	req := httptest.NewRequest("POST", "/api/registry", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusCreated {
		t.Errorf("Expected 201, got %d", w.Code)
	}
}

// --- Search Tests ---

func TestSearchByName(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	DB.Exec("INSERT INTO registry (info_hash, name, created_at) VALUES (?, ?, ?)", "h1", "Llama Weights", 100)
	DB.Exec("INSERT INTO registry (info_hash, name, created_at) VALUES (?, ?, ?)", "h2", "Climate Data", 200)

	req := httptest.NewRequest("GET", "/api/registry/search?q=Llama", nil)
	w := httptest.NewRecorder()
	HandleSearch(w, req)

	var results []registryEntry
	json.NewDecoder(w.Body).Decode(&results)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}
	if results[0].Name != "Llama Weights" {
		t.Errorf("Expected 'Llama Weights', got %q", results[0].Name)
	}
}

func TestSearchByCategory(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	DB.Exec("INSERT INTO registry (info_hash, name, created_at, category) VALUES (?, ?, ?, ?)", "h1", "Model A", 100, "models")
	DB.Exec("INSERT INTO registry (info_hash, name, created_at, category) VALUES (?, ?, ?, ?)", "h2", "Dataset B", 200, "datasets")

	req := httptest.NewRequest("GET", "/api/registry/search?category=models", nil)
	w := httptest.NewRecorder()
	HandleSearch(w, req)

	var results []registryEntry
	json.NewDecoder(w.Body).Decode(&results)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}
	if results[0].InfoHash != "h1" {
		t.Errorf("Expected h1, got %q", results[0].InfoHash)
	}
}

func TestSearchEmpty(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	req := httptest.NewRequest("GET", "/api/registry/search?q=nonexistent", nil)
	w := httptest.NewRecorder()
	HandleSearch(w, req)

	var results []registryEntry
	json.NewDecoder(w.Body).Decode(&results)

	if len(results) != 0 {
		t.Errorf("Expected 0 results, got %d", len(results))
	}
}

func TestSearchNoParams(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	DB.Exec("INSERT INTO registry (info_hash, name, created_at) VALUES (?, ?, ?)", "h1", "First", 100)
	DB.Exec("INSERT INTO registry (info_hash, name, created_at) VALUES (?, ?, ?)", "h2", "Second", 200)

	req := httptest.NewRequest("GET", "/api/registry/search", nil)
	w := httptest.NewRecorder()
	HandleSearch(w, req)

	var results []registryEntry
	json.NewDecoder(w.Body).Decode(&results)

	if len(results) != 2 {
		t.Errorf("Expected 2 results, got %d", len(results))
	}
	// Most recent first
	if results[0].Name != "Second" {
		t.Errorf("Expected 'Second' first (newest), got %q", results[0].Name)
	}
}

func TestSearchCombinedFilters(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	DB.Exec("INSERT INTO registry (info_hash, name, created_at, category) VALUES (?, ?, ?, ?)", "h1", "Llama Weights", 100, "models")
	DB.Exec("INSERT INTO registry (info_hash, name, created_at, category) VALUES (?, ?, ?, ?)", "h2", "Llama Data", 200, "datasets")
	DB.Exec("INSERT INTO registry (info_hash, name, created_at, category) VALUES (?, ?, ?, ?)", "h3", "GPT Weights", 300, "models")

	req := httptest.NewRequest("GET", "/api/registry/search?q=Llama&category=models", nil)
	w := httptest.NewRecorder()
	HandleSearch(w, req)

	var results []registryEntry
	json.NewDecoder(w.Body).Decode(&results)

	if len(results) != 1 {
		t.Fatalf("Expected 1 result, got %d", len(results))
	}
	if results[0].InfoHash != "h1" {
		t.Errorf("Expected h1, got %q", results[0].InfoHash)
	}
}

func TestSearchPagination(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	for i := 1; i <= 10; i++ {
		DB.Exec("INSERT INTO registry (info_hash, name, created_at) VALUES (?, ?, ?)",
			fmt.Sprintf("h%d", i), fmt.Sprintf("Item %d", i), 100*i)
	}

	// Test Limit
	req := httptest.NewRequest("GET", "/api/registry/search?limit=3", nil)
	w := httptest.NewRecorder()
	HandleSearch(w, req)

	if w.Header().Get("X-Total-Count") != "10" {
		t.Errorf("Expected X-Total-Count 10, got %q", w.Header().Get("X-Total-Count"))
	}

	var results []registryEntry
	json.NewDecoder(w.Body).Decode(&results)
	if len(results) != 3 {
		t.Errorf("Expected 3 results, got %d", len(results))
	}
	// Items are ordered by created_at DESC (1000, 900, 800...)
	if results[0].Name != "Item 10" {
		t.Errorf("Expected 'Item 10' first, got %q", results[0].Name)
	}

	// Test Offset
	req = httptest.NewRequest("GET", "/api/registry/search?limit=3&offset=3", nil)
	w = httptest.NewRecorder()
	HandleSearch(w, req)

	json.NewDecoder(w.Body).Decode(&results)
	if len(results) != 3 {
		t.Errorf("Expected 3 results, got %d", len(results))
	}
	if results[0].Name != "Item 7" {
		t.Errorf("Expected 'Item 7' at offset 3, got %q", results[0].Name)
	}
}

func TestSearchSorting(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	DB.Exec("INSERT INTO registry (info_hash, name, completions) VALUES (?, ?, ?)", "h1", "Few", 10)
	DB.Exec("INSERT INTO registry (info_hash, name, completions) VALUES (?, ?, ?)", "h2", "Many", 100)
	DB.Exec("INSERT INTO registry (info_hash, name, completions) VALUES (?, ?, ?)", "h3", "Medium", 50)

	req := httptest.NewRequest("GET", "/api/registry/search?sort=completions", nil)
	w := httptest.NewRecorder()
	HandleSearch(w, req)

	var results []registryEntry
	json.NewDecoder(w.Body).Decode(&results)

	if len(results) != 3 {
		t.Fatalf("Expected 3 results, got %d", len(results))
	}
	// Ordered by completions DESC: Many (100), Medium (50), Few (10)
	if results[0].Name != "Many" {
		t.Errorf("Expected 'Many' first, got %q", results[0].Name)
	}
	if results[2].Name != "Few" {
		t.Errorf("Expected 'Few' last, got %q", results[2].Name)
	}
}

func TestRegistryPostBodyTooLarge(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	// 2MB body exceeds the 1MB MaxBytesReader limit
	big := strings.Repeat("x", 2*1024*1024)
	req := httptest.NewRequest("POST", "/api/registry", strings.NewReader(big))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400 for oversized body, got %d", w.Code)
	}
}

// --- Takedown / Delete Tests ---

func TestRegistryDelete(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	old := registryKey
	registryKey = ""
	defer func() { registryKey = old }()

	// Create entry + peers
	DB.Exec("INSERT INTO registry (info_hash, name, created_at) VALUES (?, ?, ?)", "badhash", "Bad", 0)
	DB.Exec("INSERT INTO peers (info_hash, peer_id, addr, updated_at) VALUES (?, ?, ?, ?)", "badhash", "p1", "1.2.3.4:6881", 0)

	req := httptest.NewRequest("DELETE", "/api/registry?info_hash=badhash&reason=illegal", nil)
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}

	// Registry entry should be gone
	var count int
	DB.QueryRow("SELECT COUNT(*) FROM registry WHERE info_hash = ?", "badhash").Scan(&count)
	if count != 0 {
		t.Error("Registry entry should be deleted")
	}

	// Peers should be gone
	DB.QueryRow("SELECT COUNT(*) FROM peers WHERE info_hash = ?", "badhash").Scan(&count)
	if count != 0 {
		t.Error("Peers should be deleted")
	}

	// Should be in blocklist
	var reason string
	DB.QueryRow("SELECT reason FROM blocklist WHERE info_hash = ?", "badhash").Scan(&reason)
	if reason != "illegal" {
		t.Errorf("Expected reason 'illegal', got %q", reason)
	}
}

func TestRegistryDeleteRequiresKey(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	old := registryKey
	registryKey = "secret"
	defer func() { registryKey = old }()

	req := httptest.NewRequest("DELETE", "/api/registry?info_hash=h1", nil)
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Errorf("Expected 401, got %d", w.Code)
	}
}

func TestRegistryDeleteWithCorrectKey(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	old := registryKey
	registryKey = "secret"
	defer func() { registryKey = old }()

	DB.Exec("INSERT INTO registry (info_hash, name, created_at) VALUES (?, ?, ?)", "h1", "Test", 0)

	req := httptest.NewRequest("DELETE", "/api/registry?info_hash=h1", nil)
	req.Header.Set("X-Weightless-Key", "secret")
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
}

func TestHandleTorrentDownload(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	testData := []byte("fake torrent content")
	DB.Exec("INSERT INTO registry (info_hash, name, created_at, torrent_data) VALUES (?, ?, ?, ?)",
		"h1", "test", 0, testData)

	req := httptest.NewRequest("GET", "/api/registry/torrent?info_hash=h1", nil)
	w := httptest.NewRecorder()
	HandleTorrentDownload(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("Expected 200, got %d", w.Code)
	}
	if !bytesEqual(w.Body.Bytes(), testData) {
		t.Error("Downloaded torrent data mismatch")
	}
	if !strings.Contains(w.Header().Get("Content-Disposition"), "filename=\"test.torrent\"") {
		t.Errorf("Wrong filename in header: %s", w.Header().Get("Content-Disposition"))
	}
}

func bytesEqual(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRegistryDeleteDBError(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	old := registryKey
	registryKey = ""
	defer func() { registryKey = old }()

	// Insert entry so the hash param is valid
	DB.Exec("INSERT INTO registry (info_hash, name, created_at) VALUES (?, ?, ?)", "h1", "Test", 0)

	// Drop the registry table to force a DB error on DELETE
	DB.Exec("DROP TABLE registry")

	req := httptest.NewRequest("DELETE", "/api/registry?info_hash=h1&reason=test", nil)
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusInternalServerError {
		t.Errorf("Expected 500 on DB error, got %d", w.Code)
	}
}

func TestRegistryDeleteMissingHash(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	req := httptest.NewRequest("DELETE", "/api/registry", nil)
	w := httptest.NewRecorder()
	HandleAPI(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("Expected 400, got %d", w.Code)
	}
}
