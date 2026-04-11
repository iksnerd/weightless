package tracker

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"weightless/internal/torrent"
)

func TestHandleMetadata(t *testing.T) {
	DB = SetupTestDB(t)
	defer DB.Close()

	// 1. Create and register a torrent
	path := "meta-api-test"
	os.WriteFile(path, []byte("metadata test content"), 0644)
	defer os.Remove(path)

	res, err := torrent.Create(torrent.CreateOptions{
		Path:        path,
		Name:        "meta-api-test",
		PieceLength: torrent.MinPieceLength,
		AnnounceURL: "http://localhost:8080/announce",
	})
	if err != nil {
		t.Fatal(err)
	}

	_, err = DB.Exec(`INSERT INTO registry (info_hash, name, torrent_data, created_at) VALUES (?, ?, ?, ?)`,
		res.InfoHashHex, "meta-api-test", res.TorrentBytes, 12345)
	if err != nil {
		t.Fatal(err)
	}

	// 2. Test GET /api/registry/meta
	req := httptest.NewRequest("GET", "/api/registry/meta?info_hash="+res.InfoHashHex, nil)
	w := httptest.NewRecorder()
	HandleMetadata(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected 200, got %d", w.Code)
	}

	var meta torrent.TorrentMeta
	if err := json.NewDecoder(w.Body).Decode(&meta); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	if meta.Name != "meta-api-test" {
		t.Errorf("expected name meta-api-test, got %s", meta.Name)
	}
	if len(meta.Files) != 1 {
		t.Errorf("expected 1 file, got %d", len(meta.Files))
	}
}
