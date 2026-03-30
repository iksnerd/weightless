package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zeebo/bencode"
	"weightless/internal/torrent"
)

// --- Helpers ---

// testFile creates a temp file with the given content and returns its path.
func testFile(t *testing.T, name string, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatal(err)
	}
	return path
}

// testDir creates a temp directory with multiple files and returns its path.
func testDir(t *testing.T, name string, files map[string][]byte) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), name)
	os.MkdirAll(dir, 0755)
	for fname, content := range files {
		sub := filepath.Join(dir, filepath.Dir(fname))
		os.MkdirAll(sub, 0755)
		os.WriteFile(filepath.Join(dir, fname), content, 0644)
	}
	return dir
}

// registryServer returns a test server that captures the registration body.
func registryServer(t *testing.T, received *registryBody) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if received != nil {
			json.NewDecoder(r.Body).Decode(received)
		}
		w.WriteHeader(http.StatusCreated)
	}))
}

// runInDir runs runCreate and cleans up the output torrent file.
func runInDir(t *testing.T, opts createOpts) error {
	t.Helper()
	dir := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	err := runCreate(opts)

	// Clean up any generated torrent files
	matches, _ := filepath.Glob(filepath.Join(dir, "*.torrent"))
	for _, m := range matches {
		os.Remove(m)
	}
	return err
}

// readTorrent runs runCreate in a temp dir and returns the parsed torrent metadata.
func readTorrent(t *testing.T, opts createOpts) (meta map[string]interface{}, info map[string]interface{}) {
	t.Helper()
	dir := t.TempDir()
	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	if err := runCreate(opts); err != nil {
		t.Fatalf("runCreate failed: %v", err)
	}

	matches, _ := filepath.Glob(filepath.Join(dir, "*.torrent"))
	if len(matches) == 0 {
		t.Fatal("no torrent file created")
	}

	data, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatal(err)
	}
	bencode.DecodeBytes(data, &meta)
	if i, ok := meta["info"]; ok {
		info = i.(map[string]interface{})
	}
	return
}

// --- envOr ---

func TestEnvOrFallback(t *testing.T) {
	os.Unsetenv("WL_TEST_ENVOR")
	if got := envOr("WL_TEST_ENVOR", "default"); got != "default" {
		t.Errorf("expected 'default', got %q", got)
	}
}

func TestEnvOrOverride(t *testing.T) {
	t.Setenv("WL_TEST_ENVOR", "custom")
	if got := envOr("WL_TEST_ENVOR", "default"); got != "custom" {
		t.Errorf("expected 'custom', got %q", got)
	}
}

// --- Torrent creation ---

func TestCreateSingleFile(t *testing.T) {
	path := testFile(t, "test.dat", []byte("hello world data"))
	server := registryServer(t, nil)
	defer server.Close()

	err := runInDir(t, createOpts{
		path: path, name: "test.dat", trackerURL: server.URL, pieceLen: torrent.MinPieceLength,
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

func TestCreateDirectory(t *testing.T) {
	dir := testDir(t, "dataset", map[string][]byte{
		"a.txt":        []byte("hello"),
		"b.txt":        []byte("world"),
		"nested/c.txt": []byte("deep"),
	})
	server := registryServer(t, nil)
	defer server.Close()

	err := runInDir(t, createOpts{
		path: dir, trackerURL: server.URL,
	})
	if err != nil {
		t.Fatalf("expected success, got: %v", err)
	}
}

func TestCreateBadPath(t *testing.T) {
	err := runCreate(createOpts{
		path: "/nonexistent/path", trackerURL: "http://localhost:8080",
	})
	if err == nil {
		t.Error("expected error for nonexistent path")
	}
}

func TestCreateDefaultName(t *testing.T) {
	path := testFile(t, "mydata.bin", []byte("content"))
	server := registryServer(t, nil)
	defer server.Close()

	meta, _ := readTorrent(t, createOpts{
		path: path, trackerURL: server.URL, pieceLen: torrent.MinPieceLength,
	})
	// Name should default to basename
	if meta["announce"] == nil {
		t.Error("missing announce in torrent")
	}
}

// --- Torrent output verification ---

func TestOutputHasV1AndV2Fields(t *testing.T) {
	path := testFile(t, "test.dat", make([]byte, 64*1024))
	server := registryServer(t, nil)
	defer server.Close()

	_, info := readTorrent(t, createOpts{
		path: path, name: "test.dat", trackerURL: server.URL, pieceLen: torrent.MinPieceLength,
	})
	// v2
	if v, _ := info["meta version"].(int64); v != 2 {
		t.Errorf("expected meta version 2, got %v", info["meta version"])
	}
	if info["file tree"] == nil {
		t.Error("missing file tree (v2)")
	}
	// v1
	if info["pieces"] == nil {
		t.Error("missing pieces (v1)")
	}
}

func TestOutputPrivateFlag(t *testing.T) {
	path := testFile(t, "test.dat", []byte("private"))
	server := registryServer(t, nil)
	defer server.Close()

	_, info := readTorrent(t, createOpts{
		path: path, name: "test.dat", trackerURL: server.URL,
		pieceLen: torrent.MinPieceLength, private: true,
	})
	if v, _ := info["private"].(int64); v != 1 {
		t.Errorf("expected private=1, got %v", info["private"])
	}
}

func TestOutputNotPrivateByDefault(t *testing.T) {
	path := testFile(t, "test.dat", []byte("open"))
	server := registryServer(t, nil)
	defer server.Close()

	_, info := readTorrent(t, createOpts{
		path: path, name: "test.dat", trackerURL: server.URL, pieceLen: torrent.MinPieceLength,
	})
	if _, ok := info["private"]; ok {
		t.Error("torrent should not have private field by default")
	}
}

func TestOutputBranding(t *testing.T) {
	path := testFile(t, "test.dat", []byte("brand"))
	server := registryServer(t, nil)
	defer server.Close()

	t.Setenv("WL_SOURCE", "my-tracker.example.com")
	t.Setenv("WL_CREATED_BY", "TestCLI v1")

	meta, info := readTorrent(t, createOpts{
		path: path, name: "test.dat", trackerURL: server.URL, pieceLen: torrent.MinPieceLength,
	})
	if meta["created by"] != "TestCLI v1" {
		t.Errorf("expected 'TestCLI v1', got %v", meta["created by"])
	}
	if info["source"] != "my-tracker.example.com" {
		t.Errorf("expected source 'my-tracker.example.com', got %v", info["source"])
	}
}

func TestOutputNoBrandingByDefault(t *testing.T) {
	path := testFile(t, "test.dat", []byte("no brand"))
	server := registryServer(t, nil)
	defer server.Close()

	os.Unsetenv("WL_SOURCE")
	os.Unsetenv("WL_CREATED_BY")

	meta, info := readTorrent(t, createOpts{
		path: path, name: "test.dat", trackerURL: server.URL, pieceLen: torrent.MinPieceLength,
	})
	if _, ok := meta["created by"]; ok {
		t.Error("should not have 'created by' when WL_CREATED_BY is unset")
	}
	if _, ok := info["source"]; ok {
		t.Error("should not have 'source' when WL_SOURCE is unset")
	}
}

// --- Magnet link ---

func TestMagnetLinkFormat(t *testing.T) {
	path := testFile(t, "test.dat", []byte("magnet test"))
	result, _ := torrent.Create(torrent.CreateOptions{
		Path: path, Name: "test.dat", PieceLength: torrent.MinPieceLength,
		AnnounceURL: "http://localhost:8080/announce",
	})

	if !strings.Contains(result.MagnetLink, "xt=urn:btih:") {
		t.Errorf("magnet missing v1 hash: %s", result.MagnetLink)
	}
	if !strings.Contains(result.MagnetLink, "xt=urn:btmh:1220") {
		t.Errorf("magnet missing v2 hash: %s", result.MagnetLink)
	}
	if !strings.Contains(result.MagnetLink, "dn=test.dat") {
		t.Errorf("magnet missing display name: %s", result.MagnetLink)
	}
}

// --- Registry communication ---

func TestRegistrySendsJSON(t *testing.T) {
	var received registryBody
	server := registryServer(t, &received)
	defer server.Close()

	registerHash(server.URL, registryBody{
		InfoHash: "abc123", Name: "TestData", Size: 999,
	}, "")

	if received.InfoHash != "abc123" {
		t.Errorf("expected hash 'abc123', got %q", received.InfoHash)
	}
	if received.Size != 999 {
		t.Errorf("expected size 999, got %d", received.Size)
	}
}

func TestRegistrySendsMetadata(t *testing.T) {
	var received registryBody
	server := registryServer(t, &received)
	defer server.Close()

	registerHash(server.URL, registryBody{
		InfoHash: "abc", Name: "Test", Description: "A test dataset",
		Publisher: "testlab", License: "MIT", Category: "models", Tags: "llm,weights",
	}, "")

	if received.Description != "A test dataset" {
		t.Errorf("expected description, got %q", received.Description)
	}
	if received.Tags != "llm,weights" {
		t.Errorf("expected tags 'llm,weights', got %q", received.Tags)
	}
}

func TestRegistrySendsAPIKey(t *testing.T) {
	var receivedKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("X-Weightless-Key")
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	registerHash(server.URL, registryBody{InfoHash: "abc", Name: "Test"}, "my-key")
	if receivedKey != "my-key" {
		t.Errorf("expected key 'my-key', got %q", receivedKey)
	}
}

func TestRegistryNoKeyWhenEmpty(t *testing.T) {
	var receivedKey string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("X-Weightless-Key")
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	registerHash(server.URL, registryBody{InfoHash: "abc", Name: "Test"}, "")
	if receivedKey != "" {
		t.Errorf("expected no key header, got %q", receivedKey)
	}
}

func TestRegistrySendsV1Hash(t *testing.T) {
	var received registryBody
	server := registryServer(t, &received)
	defer server.Close()

	registerHash(server.URL, registryBody{
		InfoHash: "v2hash", V1InfoHash: "v1hash", Name: "Hybrid",
	}, "")

	if received.V1InfoHash != "v1hash" {
		t.Errorf("expected v1 hash 'v1hash', got %q", received.V1InfoHash)
	}
}

// --- Registry error handling ---

func TestRegistryServerError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	err := registerHash(server.URL, registryBody{InfoHash: "abc", Name: "Test"}, "")
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestRegistryServerDown(t *testing.T) {
	err := registerHash("http://127.0.0.1:1", registryBody{InfoHash: "abc", Name: "Test"}, "")
	if err == nil {
		t.Error("expected error for unreachable server")
	}
}

func TestCreateRegistryOffline(t *testing.T) {
	path := testFile(t, "data.bin", []byte("test"))

	err := runInDir(t, createOpts{
		path: path, name: "data.bin", trackerURL: "http://127.0.0.1:1",
		pieceLen: torrent.MinPieceLength,
	})
	if err != nil {
		t.Errorf("should succeed even if registry is offline: %v", err)
	}
}

// --- End-to-end: create + register ---

func TestEndToEndCreateAndRegister(t *testing.T) {
	path := testFile(t, "data.bin", []byte("end-to-end content"))
	var received registryBody
	server := registryServer(t, &received)
	defer server.Close()

	err := runInDir(t, createOpts{
		path: path, name: "data.bin", trackerURL: server.URL,
		pieceLen:    torrent.MinPieceLength,
		description: "E2E test", publisher: "tester", license: "MIT",
		category: "test", tags: "e2e,ci",
	})
	if err != nil {
		t.Fatalf("expected success: %v", err)
	}
	if received.InfoHash == "" {
		t.Error("expected registration to send info hash")
	}
	if received.V1InfoHash == "" {
		t.Error("expected registration to send v1 info hash")
	}
	if received.Description != "E2E test" {
		t.Errorf("expected description 'E2E test', got %q", received.Description)
	}
	if received.Size != 18 {
		t.Errorf("expected size 18, got %d", received.Size)
	}
	if received.TorrentData == nil {
		t.Error("expected torrent_data to be sent")
	}
}
