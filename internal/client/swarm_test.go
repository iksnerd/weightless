package client

import (
	"bytes"
	"context"
	"crypto/sha1"
	"net"
	"runtime"
	"strings"
	"sync/atomic"
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

// twoPieceFixture builds a 2-piece torrent (16-byte 'A' piece, 8-byte 'B'
// piece) plus a preallocated store, returning the meta, concatenated data, and
// info hash.
func twoPieceFixture(t *testing.T) (TorrentMeta, []byte, []byte, *Storage) {
	t.Helper()
	piece0 := bytes.Repeat([]byte{'A'}, 16)
	piece1 := bytes.Repeat([]byte{'B'}, 8)
	h0 := sha1.Sum(piece0)
	h1 := sha1.Sum(piece1)
	pieces := append(append([]byte{}, h0[:]...), h1[:]...)

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
	store := NewStorage(t.TempDir(), meta.Files)
	store.Preallocate()
	return meta, append(piece0, piece1...), infoHash, store
}

// startPeer runs an accept loop serving each connection with handle.
func startPeer(t *testing.T, handle func(net.Conn)) string {
	t.Helper()
	ln, err := listenTCP(t)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(conn)
		}
	}()
	return ln.Addr().String()
}

// deadAddr returns the address of a listener that has been closed, so dialing
// it fails fast with "connection refused".
func deadAddr(t *testing.T) string {
	t.Helper()
	ln, err := listenTCP(t)
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

// A worker that pulls a dead address from the shared pool must move on to the
// next address rather than dying — the core of the growable-pool refactor.
func TestSwarmSkipsDeadPeer(t *testing.T) {
	meta, allData, infoHash, store := twoPieceFixture(t)
	live := startTestPeer(t, allData[:16], allData[16:])

	swarm := NewSwarm(meta, 1)
	// Dead address first in the pool; the live one must still be reached.
	err := swarm.Start(context.Background(), []string{deadAddr(t), live}, infoHash, "-WL0020-test12345678", store)
	if err != nil {
		t.Fatalf("swarm should recover from a dead seed peer: %v", err)
	}
}

// PEX end-to-end: the only seeded peer serves piece 0, advertises a second
// peer via ut_pex, then chokes piece 1 — so the swarm can only finish by
// discovering the PEX peer and using it.
func TestSwarmPexDiscovery(t *testing.T) {
	meta, allData, infoHash, store := twoPieceFixture(t)

	var pexPeerConns atomic.Int64
	pexPeer := startPeer(t, func(c net.Conn) {
		pexPeerConns.Add(1)
		handleTestPeerConn(c, allData)
	})

	seed := startPeer(t, func(c net.Conn) {
		servePexThenChokePeer(t, c, allData, pexPeer, 1) // chokes piece index 1
	})

	swarm := NewSwarm(meta, 1)
	err := swarm.Start(context.Background(), []string{seed}, infoHash, "-WL0020-test12345678", store)
	if err != nil {
		t.Fatalf("swarm failed to finish via PEX peer: %v", err)
	}
	if pexPeerConns.Load() == 0 {
		t.Error("expected the swarm to connect to the PEX-advertised peer")
	}
}

// With no reachable peers and no PEX, the supervisor must end the download
// promptly with a "peer supply exhausted" error rather than hanging.
func TestSwarmExhaustsPeerSupply(t *testing.T) {
	meta, _, infoHash, store := twoPieceFixture(t)

	goroutinesBefore := runtime.NumGoroutine()

	swarm := NewSwarm(meta, 2)
	start := time.Now()
	err := swarm.Start(context.Background(), []string{deadAddr(t), deadAddr(t)}, infoHash, "-WL0020-test12345678", store)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected exhaustion error, got nil")
	}
	if !strings.Contains(err.Error(), "peer supply exhausted") {
		t.Errorf("expected 'peer supply exhausted', got: %v", err)
	}
	if elapsed > 5*time.Second {
		t.Errorf("exhaustion took too long: %v (should terminate via grace window)", elapsed)
	}

	time.Sleep(50 * time.Millisecond)
	if after := runtime.NumGoroutine(); after > goroutinesBefore+1 {
		t.Errorf("goroutine leak: before=%d, after=%d", goroutinesBefore, after)
	}
}
