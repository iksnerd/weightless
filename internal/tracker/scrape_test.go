package tracker

import (
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/zeebo/bencode"
)

func TestScrapeSingleHash(t *testing.T) {
	DB = setupTest(t)
	defer DB.Close()

	now := time.Now().Unix()
	// Update in-memory state directly as announce logic would
	State.UpdatePeer(v2Hash, "seeder1", &Peer{Addr: "1.2.3.4:6881", UpdatedAt: now, Left: 0})
	State.UpdatePeer(v2Hash, "leecher1", &Peer{Addr: "1.2.3.5:6882", UpdatedAt: now, Left: 5000})

	// Completions are still in DB for now (scrape reads from DB forDownloaded)
	DB.Exec("UPDATE registry SET completions = 42 WHERE info_hash = ?", v2Hash)

	bin, _ := hex.DecodeString(v2Hash)
	u := "/scrape?info_hash=" + url.QueryEscape(string(bin))
	req := httptest.NewRequest("GET", u, nil)
	w := httptest.NewRecorder()
	HandleScrape(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("Expected 200, got %d", w.Code)
	}

	var resp map[string]interface{}
	if err := bencode.DecodeString(w.Body.String(), &resp); err != nil {
		t.Fatalf("Decode: %v", err)
	}

	files := resp["files"].(map[string]interface{})
	// Scrape response keys are the RAW info_hash binary strings sent by client
	stats := files[string(bin)].(map[string]interface{})

	if v, _ := stats["complete"].(int64); v != 1 {
		t.Errorf("Expected complete=1, got %v", stats["complete"])
	}
	if v, _ := stats["incomplete"].(int64); v != 1 {
		t.Errorf("Expected incomplete=1, got %v", stats["incomplete"])
	}
	if v, _ := stats["downloaded"].(int64); v != 42 {
		t.Errorf("Expected downloaded=42, got %v", stats["downloaded"])
	}
}

func TestScrapeMultipleHashes(t *testing.T) {
	DB = setupTest(t)
	defer DB.Close()

	hash2 := "abcdef0123456789abcdef0123456789abcdef0123456789abcdef0123456789" // 64 valid hex chars
	DB.Exec("INSERT INTO registry (info_hash, name, created_at) VALUES (?, ?, ?)", hash2, "Torrent 2", 0)
	now := time.Now().Unix()

	State.UpdatePeer(v2Hash, "s1", &Peer{Addr: "1.2.3.4:6881", UpdatedAt: now, Left: 0})
	State.UpdatePeer(hash2, "s2", &Peer{Addr: "1.2.3.5:6882", UpdatedAt: now, Left: 100})

	bin1, _ := hex.DecodeString(v2Hash)
	bin2, _ := hex.DecodeString(hash2)

	var c int
	DB.QueryRow("SELECT COUNT(*) FROM registry WHERE info_hash = ?", hash2).Scan(&c)
	t.Logf("Registry count for hash2 (%s): %d", hash2, c)

	u := "/scrape?info_hash=" + url.QueryEscape(string(bin1)) + "&info_hash=" + url.QueryEscape(string(bin2))
	req := httptest.NewRequest("GET", u, nil)
	w := httptest.NewRecorder()
	HandleScrape(w, req)

	var resp map[string]interface{}
	bencode.DecodeString(w.Body.String(), &resp)

	files := resp["files"].(map[string]interface{})
	for k := range files {
		t.Logf("Response key (hex): %s", hex.EncodeToString([]byte(k)))
	}
	if len(files) != 2 {
		t.Errorf("Expected 2 file entries, got %d", len(files))
	}
	if _, ok := files[string(bin1)]; !ok {
		t.Errorf("Expected bin1 in response")
	}
	if _, ok := files[string(bin2)]; !ok {
		t.Errorf("Expected bin2 in response")
	}
}

func TestScrapeUnknownHash(t *testing.T) {
	DB = setupTest(t)
	defer DB.Close()

	unknownHex := "deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef" // 64 valid hex
	bin, _ := hex.DecodeString(unknownHex)

	u := "/scrape?info_hash=" + url.QueryEscape(string(bin))
	req := httptest.NewRequest("GET", u, nil)
	w := httptest.NewRecorder()
	HandleScrape(w, req)

	var resp map[string]interface{}
	bencode.DecodeString(w.Body.String(), &resp)

	files := resp["files"].(map[string]interface{})
	if _, ok := files[string(bin)]; ok {
		t.Errorf("Expected unregistered hash to be skipped")
	}
}

func TestScrapeNoHashes(t *testing.T) {
	DB = setupTest(t)
	defer DB.Close()

	req := httptest.NewRequest("GET", "/scrape", nil)
	w := httptest.NewRecorder()
	HandleScrape(w, req)

	var resp map[string]interface{}
	bencode.DecodeString(w.Body.String(), &resp)

	files := resp["files"].(map[string]interface{})
	if len(files) != 0 {
		t.Errorf("Expected empty files, got %d entries", len(files))
	}
}

func TestScrapeIgnoresInvalidHashes(t *testing.T) {
	DB = setupTest(t)
	defer DB.Close()

	// Short hash should be skipped
	bin, _ := hex.DecodeString(v2Hash)
	u := "/scrape?info_hash=tooshort&info_hash=" + url.QueryEscape(string(bin))
	req := httptest.NewRequest("GET", u, nil)
	w := httptest.NewRecorder()
	HandleScrape(w, req)

	var resp map[string]interface{}
	bencode.DecodeString(w.Body.String(), &resp)

	files := resp["files"].(map[string]interface{})
	if len(files) != 1 {
		t.Errorf("Expected 1 file entry (invalid skipped), got %d", len(files))
	}
}

func TestScrapeV1Hash(t *testing.T) {
	DB = setupTest(t)
	defer DB.Close()

	v1HashHex := "1234567890123456789012345678901234567890" // 40 hex chars
	DB.Exec("INSERT INTO registry (info_hash, v1_info_hash, name, created_at) VALUES (?, ?, ?, ?)",
		"v2-hash-padding-to-64-chars-000000000000000000000000000000000000", v1HashHex, "V1 Torrent", 0)
	now := time.Now().Unix()
	State.UpdatePeer(v1HashHex, "s1", &Peer{Addr: "1.2.3.4:6881", UpdatedAt: now, Left: 0})

	bin, _ := hex.DecodeString(v1HashHex)
	u := "/scrape?info_hash=" + url.QueryEscape(string(bin))
	req := httptest.NewRequest("GET", u, nil)
	w := httptest.NewRecorder()
	HandleScrape(w, req)

	var resp map[string]interface{}
	bencode.DecodeString(w.Body.String(), &resp)

	files := resp["files"].(map[string]interface{})
	stats := files[string(bin)].(map[string]interface{})

	if v, _ := stats["complete"].(int64); v != 1 {
		t.Errorf("Expected complete=1 for v1 hash, got %v", stats["complete"])
	}
}

func TestScrapeBlockedHash(t *testing.T) {
	DB = setupTest(t)
	defer DB.Close()

	DB.Exec("INSERT INTO blocklist (info_hash, reason, created_at) VALUES (?, ?, ?)",
		v2Hash, "banned", 0)
	State.UpdatePeer(v2Hash, "s1", &Peer{Addr: "1.2.3.4:6881", UpdatedAt: time.Now().Unix(), Left: 0})

	bin, _ := hex.DecodeString(v2Hash)
	u := "/scrape?info_hash=" + url.QueryEscape(string(bin))
	req := httptest.NewRequest("GET", u, nil)
	w := httptest.NewRecorder()
	HandleScrape(w, req)

	var resp map[string]interface{}
	bencode.DecodeString(w.Body.String(), &resp)

	files := resp["files"].(map[string]interface{})
	if len(files) != 0 {
		t.Errorf("Blocked hash should be skipped, got %d entries", len(files))
	}
}
