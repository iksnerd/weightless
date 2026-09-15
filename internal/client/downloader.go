package client

import (
	"context"
	"crypto/rand"
	"crypto/sha1"
	"encoding/binary"
	"fmt"
	"log"
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

// announceGrace bounds the best-effort completed/stopped announces. They use a
// fresh background context so a cancelled download ctx doesn't suppress them.
const announceGrace = 5 * time.Second

// DownloadMVP implements the Stage C concurrent downloader.
func DownloadMVP(ctx context.Context, opts DownloadOptions) error {
	meta := opts.Meta

	// Guard: the swarm needs one 20-byte SHA-1 per piece. A v2-only or
	// truncated torrent would otherwise make every piece fail the hash-index
	// range check in the worker, and the collector would re-enqueue them
	// forever (the exhaustion supervisor never fires while inFlight > 0).
	if len(meta.Pieces) == 0 {
		return fmt.Errorf("torrent has no v1 piece hashes (v2-only torrents are not supported yet)")
	}
	if want := meta.PieceCount * 20; len(meta.Pieces) != want {
		return fmt.Errorf("torrent piece hashes length mismatch: got %d bytes, want %d (%d pieces x 20)",
			len(meta.Pieces), want, meta.PieceCount)
	}

	peerID := GeneratePeerID()
	log.Printf("Starting download for %s", meta.Name)

	// 1. Storage Initialization
	store, err := NewStorage(opts.OutputDir, meta.Files)
	if err != nil {
		return fmt.Errorf("storage init: %w", err)
	}
	defer store.Close()
	if err := store.Preallocate(); err != nil {
		return fmt.Errorf("preallocate: %w", err)
	}

	// 2. Discover Peers
	announceURL := opts.TrackerURL
	if !strings.Contains(announceURL, "/announce") {
		announceURL = strings.TrimSuffix(announceURL, "/") + "/announce"
	}

	// Uploaded stays 0 throughout: this client never seeds, so reporting
	// anything else would be a lie to the tracker's usage accounting.
	base := AnnounceOptions{
		InfoHash: string(meta.InfoHashV1),
		PeerID:   peerID,
		Port:     6881,
		Left:     meta.TotalSize,
	}

	started := base
	started.Event = EventStarted
	addrs, err := Announce(ctx, announceURL, started)
	if err != nil {
		return fmt.Errorf("announce: %w", err)
	}
	// From here on we are registered in the swarm: every exit path must tell
	// the tracker we left, or we linger as a phantom peer until it expires us.
	stopped := base
	stopped.Event = EventStopped

	if len(addrs) == 0 {
		BestEffortAnnounce(announceURL, stopped)
		return fmt.Errorf("no peers found")
	}
	log.Printf("Found %d peers.", len(addrs))

	// 3. Swarm Download
	maxWorkers := opts.MaxWorkers
	if maxWorkers <= 0 {
		maxWorkers = 5
	}
	swarm := NewSwarm(meta, maxWorkers)
	if err := swarm.Start(ctx, addrs, meta.InfoHashV1, peerID, store); err != nil {
		BestEffortAnnounce(announceURL, stopped)
		return fmt.Errorf("swarm download: %w", err)
	}

	// 4. Report completion so the tracker counts it, then leave the swarm:
	//    we exit right after and don't seed, so staying registered would
	//    advertise a seeder that no longer exists.
	completed := base
	completed.Event = EventCompleted
	completed.Downloaded = meta.TotalSize
	completed.Left = 0
	BestEffortAnnounce(announceURL, completed)
	stopped.Downloaded = meta.TotalSize
	stopped.Left = 0
	BestEffortAnnounce(announceURL, stopped)

	fmt.Printf("\nSuccess! Downloaded %s to %s\n", meta.Name, opts.OutputDir)
	return nil
}

// BestEffortAnnounce sends a lifecycle announce (completed/stopped) that must
// never fail the caller. It runs on its own short-lived background context so
// it still fires when the caller's ctx has already been cancelled.
func BestEffortAnnounce(announceURL string, opts AnnounceOptions) {
	ctx, cancel := context.WithTimeout(context.Background(), announceGrace)
	defer cancel()
	if _, err := Announce(ctx, announceURL, opts); err != nil {
		log.Printf("announce event=%s failed (ignored): %v", opts.Event, err)
	}
}

// dialAndHandshake connects to a single peer, performs the BEP 3/10 handshake,
// and sends our initial "interested". On any failure the connection is closed
// and the error returned so the caller can move on to the next address.
func dialAndHandshake(ctx context.Context, addr string, infoHash []byte, peerID string) (*PeerConn, error) {
	p, err := Connect(ctx, addr)
	if err != nil {
		return nil, err
	}
	if err := p.Handshake(ctx, infoHash, peerID); err != nil {
		p.Close()
		return nil, err
	}
	// Send interested immediately after handshake.
	if err := p.WriteMessage(&Message{ID: MsgInterested}); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
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
		msg, err := p.ReadMessage(ctx)
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
			// Validate the block belongs where we expect before writing it, so a
			// stray/duplicate/reordered block can't land at the wrong offset.
			// Requests are issued one block at a time, in order.
			blkIndex := binary.BigEndian.Uint32(msg.Payload[0:4])
			blkBegin := binary.BigEndian.Uint32(msg.Payload[4:8])
			if blkIndex != uint32(index) || blkBegin != uint32(downloaded) {
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

		case MsgExtended:
			// BEP 11: a peer addresses ut_pex messages to us using the local
			// ID we advertised (localPexID). Harvest discovered peers; the
			// owning worker drains them after this piece finishes.
			if len(msg.Payload) > 0 && msg.Payload[0] == localPexID {
				p.handlePexMessage(msg.Payload[1:])
			}
			continue

		case MsgBitfield, MsgHave:
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

// GeneratePeerID returns a fresh 20-byte BEP 20 peer id ("-WL0020-" + 12
// random alphanumerics). Shared by the downloader and the wl CLI.
func GeneratePeerID() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, 12)
	// crypto/rand so two clients started in the same instant don't collide.
	if _, err := rand.Read(b); err != nil {
		// rand.Read never returns an error on supported platforms, but fall
		// back to a fixed-but-valid suffix rather than panicking.
		return "-WL0020-aaaaaaaaaaaa"
	}
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return "-WL0020-" + string(b)
}
