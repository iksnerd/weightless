package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"weightless/internal/torrent"
)

func TestRunCreateSingleFile(t *testing.T) {
	dir := t.TempDir()
	dataFile := filepath.Join(dir, "test.dat")
	os.WriteFile(dataFile, []byte("hello world data for testing"), 0644)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	err := runCreate(createOpts{
		path:       dataFile,
		name:       "test.dat",
		trackerURL: server.URL,
		pieceLen:   torrent.MinPieceLength,
	})
	if err != nil {
		t.Fatalf("runCreate failed: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "test.dat.torrent")); os.IsNotExist(err) {
		t.Error("torrent file not created")
	}
}

func TestRegistrationSendsJSON(t *testing.T) {
	var received registryBody

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	err := registerHash(server.URL, registryBody{
		InfoHash: "abc123",
		Name:     "TestData",
		Size:     999,
	}, "")
	if err != nil {
		t.Fatalf("registerHash failed: %v", err)
	}
	if received.InfoHash != "abc123" {
		t.Errorf("expected hash abc123, got %s", received.InfoHash)
	}
	if received.Name != "TestData" {
		t.Errorf("expected name TestData, got %s", received.Name)
	}
	if received.Size != 999 {
		t.Errorf("expected size 999, got %d", received.Size)
	}
}

func TestRegistrationSendsMetadata(t *testing.T) {
	var received registryBody

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	err := registerHash(server.URL, registryBody{
		InfoHash:    "abc123",
		Name:        "Test",
		Description: "A test dataset",
		Publisher:   "testlab",
		License:     "MIT",
		Category:    "models",
		Tags:        "llm,weights",
	}, "")
	if err != nil {
		t.Fatalf("registerHash failed: %v", err)
	}
	if received.Description != "A test dataset" {
		t.Errorf("expected description, got %q", received.Description)
	}
	if received.Category != "models" {
		t.Errorf("expected category 'models', got %q", received.Category)
	}
}

func TestRegistrationGracefulFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	err := registerHash(server.URL, registryBody{InfoHash: "abc", Name: "Test"}, "")
	if err == nil {
		t.Error("expected error for 500 response")
	}
}

func TestRegistrationServerDown(t *testing.T) {
	err := registerHash("http://127.0.0.1:1", registryBody{InfoHash: "abc", Name: "Test"}, "")
	if err == nil {
		t.Error("expected error for unreachable server")
	}
}

func TestRunCreateWithRegistry(t *testing.T) {
	dir := t.TempDir()
	dataFile := filepath.Join(dir, "data.bin")
	os.WriteFile(dataFile, []byte("some test content"), 0644)

	var received registryBody
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		json.NewDecoder(r.Body).Decode(&received)
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	err := runCreate(createOpts{
		path:       dataFile,
		name:       "data.bin",
		trackerURL: server.URL,
		pieceLen:   torrent.MinPieceLength,
	})
	if err != nil {
		t.Fatalf("runCreate failed: %v", err)
	}
	if received.InfoHash == "" {
		t.Error("expected registration to occur")
	}
	if received.Size != 17 {
		t.Errorf("expected size 17, got %d", received.Size)
	}
}

func TestRunCreateRegistryOffline(t *testing.T) {
	dir := t.TempDir()
	dataFile := filepath.Join(dir, "data.bin")
	os.WriteFile(dataFile, []byte("test"), 0644)

	orig, _ := os.Getwd()
	os.Chdir(dir)
	defer os.Chdir(orig)

	err := runCreate(createOpts{
		path:       dataFile,
		name:       "data.bin",
		trackerURL: "http://127.0.0.1:1",
		pieceLen:   torrent.MinPieceLength,
	})
	if err != nil {
		t.Errorf("should succeed even if registry is offline: %v", err)
	}

	if _, err := os.Stat(filepath.Join(dir, "data.bin.torrent")); os.IsNotExist(err) {
		t.Error("torrent file should exist even when registry is offline")
	}
}

func TestRegistrationSendsAPIKey(t *testing.T) {
	var receivedKey string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedKey = r.Header.Get("X-Weightless-Key")
		w.WriteHeader(http.StatusCreated)
	}))
	defer server.Close()

	registerHash(server.URL, registryBody{InfoHash: "abc", Name: "Test"}, "my-secret-key")
	if receivedKey != "my-secret-key" {
		t.Errorf("expected key 'my-secret-key', got %q", receivedKey)
	}
}

func TestMagnetLinkOutput(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")
	os.WriteFile(path, []byte("magnet test data"), 0644)

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
