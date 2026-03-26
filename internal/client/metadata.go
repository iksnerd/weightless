package client

import (
	"context"
	"crypto/sha1"
	"fmt"

	"github.com/zeebo/bencode"
)

// FetchMetadata fetches the info dictionary from a peer using BEP 9 metadata exchange.
func (p *PeerConn) FetchMetadata(ctx context.Context, infoHash []byte) ([]byte, error) {
	if p.MetadataSize == 0 {
		return nil, fmt.Errorf("peer did not provide metadata_size")
	}

	numPieces := (p.MetadataSize + 16383) / 16384
	metadata := make([]byte, p.MetadataSize)

	for i := 0; i < numPieces; i++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if err := p.RequestMetadata(i); err != nil {
			return nil, err
		}

		msg, err := p.ReadMessage()
		if err != nil {
			return nil, err
		}

		if msg == KeepAlive || msg.ID != MsgExtended {
			return nil, fmt.Errorf("expected extended message, got %v", msg)
		}

		if len(msg.Payload) < 2 {
			return nil, fmt.Errorf("metadata response too short")
		}

		// BEP 9 payload: <ext_id (1 byte)><bencoded dict><raw piece data>
		// Find where the bencoded dict ends by scanning for the matching 'e'.
		dictEnd := findBencodeEnd(msg.Payload[1:])
		if dictEnd < 0 {
			return nil, fmt.Errorf("could not find end of bencoded header in piece %d", i)
		}

		// Decode the header to check msg_type
		var header struct {
			MsgType int `bencode:"msg_type"`
			Piece   int `bencode:"piece"`
			Total   int `bencode:"total_size"`
		}
		if err := bencode.DecodeBytes(msg.Payload[1:1+dictEnd], &header); err != nil {
			return nil, fmt.Errorf("decode metadata header: %w", err)
		}

		if header.MsgType == 2 { // Reject
			return nil, fmt.Errorf("peer rejected metadata request for piece %d", i)
		}
		if header.MsgType != 1 { // Data
			return nil, fmt.Errorf("unexpected metadata msg_type %d", header.MsgType)
		}

		// Everything after the dict is the raw piece data
		pieceData := msg.Payload[1+dictEnd:]
		if len(pieceData) == 0 {
			return nil, fmt.Errorf("metadata piece %d has no data", i)
		}

		copy(metadata[i*16384:], pieceData)
	}

	// Verify info_hash
	hash := sha1.Sum(metadata)
	if string(hash[:]) != string(infoHash) {
		return nil, fmt.Errorf("metadata hash mismatch")
	}

	return metadata, nil
}

// findBencodeEnd scans a byte slice starting with a bencoded value and returns
// the index one past the end of that value. Returns -1 if parsing fails.
// Handles: dicts (d...e), lists (l...e), ints (i...e), strings (N:...).
func findBencodeEnd(b []byte) int {
	if len(b) == 0 {
		return -1
	}

	pos := 0
	depth := 0

	for pos < len(b) {
		switch {
		case b[pos] == 'd' || b[pos] == 'l':
			depth++
			pos++
		case b[pos] == 'e':
			depth--
			pos++
			if depth == 0 {
				return pos
			}
		case b[pos] == 'i':
			// Integer: i<digits>e
			end := pos + 1
			for end < len(b) && b[end] != 'e' {
				end++
			}
			if end >= len(b) {
				return -1
			}
			pos = end + 1 // skip past 'e'
		case b[pos] >= '0' && b[pos] <= '9':
			// String: <length>:<data>
			numEnd := pos
			for numEnd < len(b) && b[numEnd] >= '0' && b[numEnd] <= '9' {
				numEnd++
			}
			if numEnd >= len(b) || b[numEnd] != ':' {
				return -1
			}
			length := 0
			for _, c := range b[pos:numEnd] {
				next := length*10 + int(c-'0')
				if next < length { // integer overflow
					return -1
				}
				length = next
			}
			if numEnd+1+length > len(b) {
				return -1
			}
			pos = numEnd + 1 + length // skip past ":"  + data
		default:
			return -1
		}

		if depth == 0 {
			return pos
		}
	}

	return -1
}
