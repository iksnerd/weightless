package client

import (
	"context"
	"fmt"
	"log"
	"sync"
)

// PieceStatus represents the state of a piece in the swarm.
type PieceStatus int

const (
	PiecePending PieceStatus = iota
	PieceInProgress
	PieceCompleted
)

// Swarm manages multiple peer connections and coordinates the download.
type Swarm struct {
	meta       TorrentMeta
	maxWorkers int
	pieces     []PieceStatus
	mu         sync.Mutex

	workChan   chan int         // Channel to dispatch piece indices to workers
	resultChan chan pieceResult // Channel to receive downloaded pieces
}

type pieceResult struct {
	index int
	data  []byte
	err   error
}

// NewSwarm creates a new swarm manager.
func NewSwarm(meta TorrentMeta, maxWorkers int) *Swarm {
	return &Swarm{
		meta:       meta,
		maxWorkers: maxWorkers,
		pieces:     make([]PieceStatus, meta.PieceCount),
		workChan:   make(chan int, meta.PieceCount),
		resultChan: make(chan pieceResult),
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

	// 2. Spawn workers
	maxWorkers := s.maxWorkers
	if len(addrs) < maxWorkers {
		maxWorkers = len(addrs)
	}

	var wg sync.WaitGroup
	for i := 0; i < maxWorkers; i++ {
		wg.Add(1)
		// We distribute addresses among workers
		workerAddrs := []string{}
		for j := i; j < len(addrs); j += maxWorkers {
			workerAddrs = append(workerAddrs, addrs[j])
		}
		go func(addrs []string) {
			defer wg.Done()
			s.worker(ctx, addrs, infoHash, peerID)
		}(workerAddrs)
	}

	// 3. Close resultChan after all workers exit
	go func() {
		wg.Wait()
		close(s.resultChan)
	}()

	// 4. Collection loop — exits when resultChan is closed (all workers done)
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
		log.Printf("Progress: [%d/%d] pieces verified", completed, s.meta.PieceCount)

		if completed == s.meta.PieceCount {
			cancel() // signal workers to stop
			break
		}
	}

	// 5. Drain remaining results so wg goroutine can close resultChan
	for range s.resultChan {
	}

	if dlErr != nil {
		return dlErr
	}
	if completed < s.meta.PieceCount {
		return fmt.Errorf("all workers died before download completed (%d/%d)", completed, s.meta.PieceCount)
	}
	return nil
}

func (s *Swarm) worker(ctx context.Context, addrs []string, infoHash []byte, peerID string) {
	var conn *PeerConn
	var currentAddr string
	defer func() {
		if conn != nil {
			conn.Close()
		}
	}()

	for {
		select {
		case index := <-s.workChan:
			s.mu.Lock()
			if s.pieces[index] != PiecePending {
				s.mu.Unlock()
				continue
			}
			s.pieces[index] = PieceInProgress
			s.mu.Unlock()

			// Ensure we have a connection
			if conn == nil {
				var err error
				conn, currentAddr, err = connectToAny(ctx, addrs, infoHash, peerID)
				if err != nil {
					log.Printf("Worker failed to connect to any peer. Exiting.")
					s.resultChan <- pieceResult{index: index, err: err}
					return
				}
				log.Printf("Worker connected to %s", currentAddr)
			}

			// Download piece
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
			if err != nil {
				log.Printf("Worker piece %d failed from %s: %v", index, currentAddr, err)
				conn.Close()
				conn = nil
				s.resultChan <- pieceResult{index: index, err: err}
				continue
			}

			s.resultChan <- pieceResult{index: index, data: data}

		case <-ctx.Done():
			return
		}
	}
}
