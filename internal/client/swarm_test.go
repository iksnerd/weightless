package client

import (
	"context"
	"crypto/sha1"
	"runtime"
	"testing"
	"time"
)

func TestSwarmShutdownNoLeak(t *testing.T) {
	// Create a 2-piece "torrent" with known hashes
	piece0 := make([]byte, 16)
	piece1 := make([]byte, 8)
	for i := range piece0 {
		piece0[i] = 'A'
	}
	for i := range piece1 {
		piece1[i] = 'B'
	}
	h0 := sha1.Sum(piece0)
	h1 := sha1.Sum(piece1)
	var pieces []byte
	pieces = append(pieces, h0[:]...)
	pieces = append(pieces, h1[:]...)

	infoHash := make([]byte, 20)
	copy(infoHash, "testhash1234567890ab")

	meta := TorrentMeta{
		Name:        "test",
		InfoHashV1:  infoHash,
		PieceLength: 16,
		PieceCount:  2,
		TotalSize:   24,
		Pieces:      pieces,
		Files:       []FileEntry{{Path: "test.dat", Length: 24}},
	}

	// Start a mock peer that serves the data
	peerAddr := startTestPeer(t, piece0, piece1)

	store := NewStorage(t.TempDir(), meta.Files)
	store.Preallocate()

	goroutinesBefore := runtime.NumGoroutine()

	swarm := NewSwarm(meta, 1)
	err := swarm.Start(context.Background(), []string{peerAddr}, infoHash, "-WL0020-test12345678", store)
	if err != nil {
		t.Fatalf("swarm failed: %v", err)
	}

	// Give goroutines a moment to wind down
	time.Sleep(50 * time.Millisecond)
	goroutinesAfter := runtime.NumGoroutine()

	// Allow 1 goroutine margin for GC, timers, etc.
	if goroutinesAfter > goroutinesBefore+1 {
		t.Errorf("goroutine leak: before=%d, after=%d", goroutinesBefore, goroutinesAfter)
	}
}

// startTestPeer starts a mock peer that serves pieces sequentially.
// It handles BEP 3 handshake, BEP 10 extended handshake, and piece requests.
func startTestPeer(t *testing.T, pieces ...[]byte) string {
	t.Helper()
	// Concatenate all piece data for offset-based serving
	var allData []byte
	for _, p := range pieces {
		allData = append(allData, p...)
	}

	ln, err := listenTCP(t)
	if err != nil {
		t.Fatal(err)
	}

	go func() {
		defer ln.Close()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handleTestPeerConn(conn, allData)
		}
	}()

	return ln.Addr().String()
}
