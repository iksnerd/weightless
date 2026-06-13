package client

import (
	"context"
	"fmt"
	"log"
	"sync"
	"sync/atomic"
	"time"
)

// PieceStatus represents the state of a piece in the swarm.
type PieceStatus int

const (
	PiecePending PieceStatus = iota
	PieceInProgress
	PieceCompleted
)

const (
	// peerPoolHeadroom is the buffered capacity of the shared peer pool. It
	// holds the initial seed plus PEX (BEP 11) discoveries; trackers cap a
	// swarm's returned peers well below this, and a full pool simply drops
	// further additions (they can be re-offered later).
	peerPoolHeadroom = 256
	// exhaustionPoll is how often the supervisor checks for a stalled swarm.
	exhaustionPoll = 250 * time.Millisecond
	// exhaustionConfirms is the number of consecutive positive polls required
	// before declaring the peer supply exhausted. Requiring two (≈500ms of
	// sustained idle) absorbs the microsecond gap between a worker pulling the
	// last address and marking itself in-flight, so a connect about to start
	// can't be mistaken for a dead swarm.
	exhaustionConfirms = 2
)

// Swarm manages multiple peer connections and coordinates the download.
type Swarm struct {
	meta       TorrentMeta
	maxWorkers int
	pieces     []PieceStatus
	mu         sync.Mutex

	workChan   chan int         // Channel to dispatch piece indices to workers
	resultChan chan pieceResult // Channel to receive downloaded pieces

	// Shared, growable peer pool. peerChan feeds addresses to any idle worker;
	// knownAddrs (guarded by poolMu) dedupes both the initial seed and PEX
	// discoveries. inFlight counts workers currently holding or acquiring a
	// connection; completed counts verified pieces. Both are read by the
	// exhaustion supervisor.
	peerChan   chan string
	knownAddrs map[string]bool
	poolMu     sync.Mutex
	inFlight   atomic.Int64
	completed  atomic.Int64
	exhausted  atomic.Bool
}

type pieceResult struct {
	index int
	data  []byte
	err   error
}

// NewSwarm creates a new swarm manager. The peer pool is initialized here so
// AddPeers is safe to call before or during Start (e.g. PEX discoveries).
func NewSwarm(meta TorrentMeta, maxWorkers int) *Swarm {
	return &Swarm{
		meta:       meta,
		maxWorkers: maxWorkers,
		pieces:     make([]PieceStatus, meta.PieceCount),
		workChan:   make(chan int, meta.PieceCount),
		resultChan: make(chan pieceResult),
		peerChan:   make(chan string, peerPoolHeadroom),
		knownAddrs: make(map[string]bool),
	}
}

// AddPeers dedupes the given addresses against the known set and pushes the
// fresh ones onto the shared pool. The push is non-blocking: if the pool is
// full the address is dropped and left un-known, so it can be re-offered later.
func (s *Swarm) AddPeers(addrs []string) {
	for _, addr := range addrs {
		s.poolMu.Lock()
		if s.knownAddrs[addr] {
			s.poolMu.Unlock()
			continue
		}
		select {
		case s.peerChan <- addr:
			s.knownAddrs[addr] = true // mark known only once it's actually queued
		default:
		}
		s.poolMu.Unlock()
	}
}

// Start kicks off the swarm download. Cancelling ctx stops all workers.
func (s *Swarm) Start(ctx context.Context, addrs []string, infoHash []byte, peerID string, store *Storage) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// 1. Initialize piece queue
	for i := 0; i < s.meta.PieceCount; i++ {
		s.workChan <- i
	}

	// 2. Seed the shared peer pool (the pool itself was created in NewSwarm).
	s.AddPeers(addrs)

	// 3. Spawn a fixed pool of workers. Unlike the old static partition, every
	//    worker pulls from the shared peerChan, so idle workers block until the
	//    pool (or PEX) supplies a peer rather than dying.
	maxWorkers := s.maxWorkers
	if maxWorkers < 1 {
		maxWorkers = 1
	}

	var wg sync.WaitGroup
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			s.worker(ctx, infoHash, peerID)
		}()
	}

	// 4. Supervisor: cancel the download if the peer supply is exhausted while
	//    pieces remain (workers no longer die on connect failure, so the swarm
	//    can't end itself the way it used to).
	supDone := make(chan struct{})
	go func() {
		defer close(supDone)
		s.superviseExhaustion(ctx, cancel)
	}()

	// 5. Close resultChan after all workers exit
	go func() {
		wg.Wait()
		close(s.resultChan)
	}()

	// 6. Collection loop — exits when resultChan is closed (all workers done)
	var dlErr error
	completed := 0
	for res := range s.resultChan {
		if res.err != nil {
			log.Printf("Piece %d failed: %v. Re-enqueuing...", res.index, res.err)
			s.mu.Lock()
			s.pieces[res.index] = PiecePending
			s.mu.Unlock()
			select {
			case s.workChan <- res.index:
			case <-ctx.Done():
			}
			continue
		}

		// Write to disk
		if err := store.WritePiece(res.index, s.meta.PieceLength, res.data); err != nil {
			dlErr = err
			cancel() // signal workers to stop
			break
		}

		s.mu.Lock()
		s.pieces[res.index] = PieceCompleted
		s.mu.Unlock()
		completed++
		s.completed.Store(int64(completed))
		log.Printf("Progress: [%d/%d] pieces verified", completed, s.meta.PieceCount)

		if completed == s.meta.PieceCount {
			cancel() // signal workers to stop
			break
		}
	}

	// 7. Drain remaining results so wg goroutine can close resultChan
	for range s.resultChan {
	}

	// 8. Stop the supervisor and wait for it before returning (no leak).
	cancel()
	<-supDone

	if dlErr != nil {
		return dlErr
	}
	if completed < s.meta.PieceCount {
		if s.exhausted.Load() {
			return fmt.Errorf("peer supply exhausted (%d/%d pieces)", completed, s.meta.PieceCount)
		}
		return fmt.Errorf("all workers died before download completed (%d/%d)", completed, s.meta.PieceCount)
	}
	return nil
}

// superviseExhaustion polls for a stalled swarm: the pool is empty, no worker
// holds or is acquiring a connection, and pieces remain. When that holds for
// exhaustionConfirms consecutive polls it cancels the download.
func (s *Swarm) superviseExhaustion(ctx context.Context, cancel context.CancelFunc) {
	t := time.NewTicker(exhaustionPoll)
	defer t.Stop()

	confirms := 0
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			stalled := len(s.peerChan) == 0 &&
				s.inFlight.Load() == 0 &&
				s.completed.Load() < int64(s.meta.PieceCount)
			if !stalled {
				confirms = 0
				continue
			}
			confirms++
			if confirms >= exhaustionConfirms {
				s.exhausted.Store(true)
				cancel()
				return
			}
		}
	}
}

// worker acquires a peer connection from the shared pool, then downloads pieces
// over it until the connection dies (re-acquire) or the context is cancelled.
func (s *Swarm) worker(ctx context.Context, infoHash []byte, peerID string) {
	var conn *PeerConn
	var currentAddr string
	defer func() {
		if conn != nil {
			conn.Close()
			s.inFlight.Add(-1) // release the in-flight count this conn held
		}
	}()

	for {
		// Acquire a connection BEFORE pulling work, so a worker waiting on the
		// pool can't strand a piece marked in-progress behind it.
		if conn == nil {
			var ok bool
			conn, currentAddr, ok = s.acquireConn(ctx, infoHash, peerID)
			if !ok {
				return // ctx cancelled while waiting for a peer
			}
			log.Printf("Worker connected to %s", currentAddr)
		}

		select {
		case index := <-s.workChan:
			s.mu.Lock()
			if s.pieces[index] != PiecePending {
				s.mu.Unlock()
				continue
			}
			s.pieces[index] = PieceInProgress
			s.mu.Unlock()

			pieceSize := s.meta.PieceLength
			remaining := s.meta.TotalSize - int64(index)*int64(s.meta.PieceLength)
			if remaining < int64(pieceSize) {
				pieceSize = int(remaining)
			}
			if (index+1)*20 > len(s.meta.Pieces) {
				s.resultChan <- pieceResult{index: index, err: fmt.Errorf("piece %d: hash index out of range (pieces len=%d)", index, len(s.meta.Pieces))}
				continue
			}
			expectedHash := s.meta.Pieces[index*20 : (index+1)*20]

			data, err := downloadPiece(ctx, conn, index, pieceSize, expectedHash)
			// Whatever the outcome, harvest any peers this peer exchanged.
			s.AddPeers(conn.DrainPexPeers())
			if err != nil {
				log.Printf("Worker piece %d failed from %s: %v", index, currentAddr, err)
				conn.Close()
				conn = nil
				s.inFlight.Add(-1) // this connection is gone; we'll re-acquire
				s.resultChan <- pieceResult{index: index, err: err}
				continue
			}

			s.resultChan <- pieceResult{index: index, data: data}

		case <-ctx.Done():
			return
		}
	}
}

// acquireConn pulls addresses from the shared pool and dials them until one
// completes the handshake. Returns ok=false if the context is cancelled while
// waiting. A successful return leaves inFlight incremented for the returned
// connection; the caller (worker) is responsible for the matching decrement.
func (s *Swarm) acquireConn(ctx context.Context, infoHash []byte, peerID string) (*PeerConn, string, bool) {
	for {
		select {
		case <-ctx.Done():
			return nil, "", false
		case addr := <-s.peerChan:
			// Count this attempt as in-flight from the moment we take the
			// address, before dialing — the supervisor must never see an empty
			// pool with a connect in progress and call the swarm dead.
			s.inFlight.Add(1)
			conn, err := dialAndHandshake(ctx, addr, infoHash, peerID)
			if err != nil {
				log.Printf("Worker failed to connect to %s: %v", addr, err)
				s.inFlight.Add(-1)
				continue
			}
			return conn, addr, true
		}
	}
}
