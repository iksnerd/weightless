package client

import (
	"context"
	"crypto/sha1"
	"fmt"
	"log"
	"math/rand"
	"strings"
	"time"
)

const (
	// BlockSize is the standard BitTorrent request block size (16 KiB)
	BlockSize = 16384
)

// DownloadOptions configures the downloader.
type DownloadOptions struct {
	Meta       TorrentMeta
	TrackerURL string
	OutputDir  string
	MaxWorkers int // Max concurrent peer connections (default: 5)
}

// DownloadMVP implements the Stage C concurrent downloader.
func DownloadMVP(ctx context.Context, opts DownloadOptions) error {
	peerID := generatePeerID()
	log.Printf("Starting download for %s", opts.Meta.Name)

	// 1. Storage Initialization
	store := NewStorage(opts.OutputDir, opts.Meta.Files)
	if err := store.Preallocate(); err != nil {
		return fmt.Errorf("preallocate: %w", err)
	}

	// 2. Discover Peers
	announceURL := opts.TrackerURL
	if !strings.Contains(announceURL, "/announce") {
		announceURL = strings.TrimSuffix(announceURL, "/") + "/announce"
	}

	addrs, err := Announce(ctx, announceURL, string(opts.Meta.InfoHashV1), peerID, 6881, opts.Meta.TotalSize)
	if err != nil {
		return fmt.Errorf("announce: %w", err)
	}
	if len(addrs) == 0 {
		return fmt.Errorf("no peers found")
	}
	log.Printf("Found %d peers.", len(addrs))

	// 3. Swarm Download
	maxWorkers := opts.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = 5
	}
	swarm := NewSwarm(opts.Meta, maxWorkers)
	if err := swarm.Start(ctx, addrs, opts.Meta.InfoHashV1, peerID, store); err != nil {
		return fmt.Errorf("swarm download: %w", err)
	}

	fmt.Printf("\nSuccess! Downloaded %s to %s\n", opts.Meta.Name, opts.OutputDir)
	return nil
}

// connectToAny tries each address until one succeeds handshake.
func connectToAny(ctx context.Context, addrs []string, infoHash []byte, peerID string) (*PeerConn, string, error) {
	var lastErr error
	for _, addr := range addrs {
		p, err := Connect(ctx, addr)
		if err != nil {
			lastErr = err
			continue
		}
		if err := p.Handshake(ctx, infoHash, peerID); err != nil {
			p.Close()
			lastErr = err
			continue
		}
		// Send interested immediately after handshake
		if err := p.WriteMessage(&Message{ID: MsgInterested}); err != nil {
			p.Close()
			lastErr = err
			continue
		}
		return p, addr, nil
	}
	return nil, "", fmt.Errorf("all peers failed: %w", lastErr)
}

// downloadPiece downloads and verifies a single piece over an existing connection.
func downloadPiece(ctx context.Context, p *PeerConn, index int, size int, expectedHash []byte) ([]byte, error) {
	pieceData := make([]byte, size)
	downloaded := 0

	// If already unchoked (from a previous piece), request immediately
	if !p.PeerChoking {
		reqLen := blockSize(size, downloaded)
		if err := p.WriteMessage(FormatRequest(uint32(index), uint32(downloaded), uint32(reqLen))); err != nil {
			return nil, err
		}
	}

	for downloaded < size {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		msg, err := p.ReadMessage()
		if err != nil {
			return nil, err
		}

		if msg == KeepAlive {
			continue
		}

		switch msg.ID {
		case MsgUnchoke:
			p.PeerChoking = false
			// Request first block now that we're unchoked
			if downloaded == 0 {
				reqLen := blockSize(size, downloaded)
				if err := p.WriteMessage(FormatRequest(uint32(index), uint32(downloaded), uint32(reqLen))); err != nil {
					return nil, err
				}
			}

		case MsgChoke:
			p.PeerChoking = true
			return nil, fmt.Errorf("peer choked us")

		case MsgPiece:
			if len(msg.Payload) < 8 {
				continue
			}
			block := msg.Payload[8:]
			if downloaded+len(block) > size {
				return nil, fmt.Errorf("received block too large")
			}
			copy(pieceData[downloaded:], block)
			downloaded += len(block)

			if downloaded < size {
				reqLen := blockSize(size, downloaded)
				if err := p.WriteMessage(FormatRequest(uint32(index), uint32(downloaded), uint32(reqLen))); err != nil {
					return nil, err
				}
			}

		case MsgExtended, MsgBitfield, MsgHave:
			continue
		}
	}

	// Verify
	hash := sha1.Sum(pieceData)
	if string(hash[:]) != string(expectedHash) {
		return nil, fmt.Errorf("hash mismatch")
	}

	return pieceData, nil
}

// blockSize returns the request length for the next block, capped to remaining piece bytes.
func blockSize(pieceSize, downloaded int) int {
	rem := pieceSize - downloaded
	if rem > BlockSize {
		return BlockSize
	}
	return rem
}

func formatPieceSize(n int) string {
	if n >= 1<<20 {
		return fmt.Sprintf("%.1f MB", float64(n)/float64(1<<20))
	}
	if n >= 1<<10 {
		return fmt.Sprintf("%.1f KB", float64(n)/float64(1<<10))
	}
	return fmt.Sprintf("%d B", n)
}

func generatePeerID() string {
	r := rand.New(rand.NewSource(time.Now().UnixNano()))
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 12)
	for i := range b {
		b[i] = charset[r.Intn(len(charset))]
	}
	return "-WL0020-" + string(b)
}
