package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/zeebo/bencode"
)

// Extension ID mapping for BEP 10
const (
	ExtMetadata = "ut_metadata"
)

// PeerConn wraps a TCP connection to a peer and tracks PWP state.
type PeerConn struct {
	conn net.Conn
	addr string

	AmChoking      bool
	AmInterested   bool
	PeerChoking    bool
	PeerInterested bool

	// BEP 10 Extension data
	PeerExtensions map[string]int
	MetadataSize   int
}

// Connect establishes a TCP connection to the given address.
func Connect(ctx context.Context, addr string) (*PeerConn, error) {
	d := net.Dialer{Timeout: 10 * time.Second}
	conn, err := d.DialContext(ctx, "tcp", addr)
	if err != nil {
		return nil, err
	}

	return &PeerConn{
		conn:        conn,
		addr:        addr,
		AmChoking:   true,
		PeerChoking: true,
	}, nil
}

// Handshake performs the standard BEP 3 handshake.
// Sends: <pstrlen><pstr><reserved><info_hash><peer_id>
//
// NOTE: Even for hybrid v1+v2 torrents, the BitTorrent wire protocol
// handshake ALWAYS uses the 20-byte SHA-1 info hash (v1) per BEP 3.
func (p *PeerConn) Handshake(ctx context.Context, infoHash []byte, peerID string) error {
	deadline := time.Now().Add(10 * time.Second)
	if d, ok := ctx.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}
	p.conn.SetDeadline(deadline)
	defer p.conn.SetDeadline(time.Time{})

	if len(infoHash) != 20 {
		return fmt.Errorf("info hash must be 20 bytes (v1), got %d", len(infoHash))
	}
	if len(peerID) != 20 {
		return fmt.Errorf("peer id must be 20 bytes, got %d", len(peerID))
	}

	pstr := "BitTorrent protocol"
	buf := make([]byte, 1+len(pstr)+8+20+20)
	buf[0] = byte(len(pstr))
	curr := 1
	curr += copy(buf[curr:], pstr)

	// Reserved bytes (8 bytes)
	// We set bit 43 (byte 5, bit 0x10) to signal BEP 10 Extension Protocol support.
	reserved := make([]byte, 8)
	reserved[5] |= 0x10
	curr += copy(buf[curr:], reserved)

	curr += copy(buf[curr:], infoHash)
	copy(buf[curr:], peerID)

	if _, err := p.conn.Write(buf); err != nil {
		return fmt.Errorf("write handshake: %w", err)
	}

	// Read peer's handshake
	resBuf := make([]byte, 1)
	if _, err := io.ReadFull(p.conn, resBuf); err != nil {
		return fmt.Errorf("read pstrlen: %w", err)
	}
	pstrlen := int(resBuf[0])
	if pstrlen == 0 {
		return fmt.Errorf("invalid pstrlen 0")
	}

	resBuf = make([]byte, pstrlen+8+20+20)
	if _, err := io.ReadFull(p.conn, resBuf); err != nil {
		return fmt.Errorf("read handshake payload: %w", err)
	}

	resInfoHash := resBuf[pstrlen+8 : pstrlen+8+20]
	if !bytes.Equal(resInfoHash, infoHash) {
		return fmt.Errorf("info hash mismatch: expected %x, got %x", infoHash, resInfoHash)
	}

	// Check if peer supports BEP 10
	peerReserved := resBuf[pstrlen : pstrlen+8]
	supportsBEP10 := (peerReserved[5] & 0x10) != 0

	if supportsBEP10 {
		// Send our BEP 10 extended handshake
		if err := p.sendExtendedHandshake(); err != nil {
			return fmt.Errorf("send extended handshake: %w", err)
		}

		// Read the peer's extended handshake response
		if err := p.readExtendedHandshake(); err != nil {
			return fmt.Errorf("read extended handshake: %w", err)
		}
	}

	return nil
}

// readExtendedHandshake reads the peer's BEP 10 handshake dictionary.
func (p *PeerConn) readExtendedHandshake() error {
	m, err := p.ReadMessage()
	if err != nil {
		return err
	}
	if m == KeepAlive {
		return fmt.Errorf("expected extended handshake, got keep-alive")
	}
	if m.ID != MsgExtended {
		return fmt.Errorf("expected extended message (20), got %d", m.ID)
	}
	if len(m.Payload) < 2 {
		return fmt.Errorf("extended message payload too short")
	}
	if m.Payload[0] != 0 {
		return fmt.Errorf("expected extended handshake (id 0), got %d", m.Payload[0])
	}

	var handshake struct {
		M        map[string]int `bencode:"m"`
		Metadata int            `bencode:"metadata_size"`
	}
	if err := bencode.DecodeBytes(m.Payload[1:], &handshake); err != nil {
		return fmt.Errorf("decode extended handshake: %w", err)
	}

	p.PeerExtensions = handshake.M
	p.MetadataSize = handshake.Metadata
	return nil
}

// RequestMetadata sends a BEP 9 metadata request for the given piece.
func (p *PeerConn) RequestMetadata(piece int) error {
	extID, ok := p.PeerExtensions[ExtMetadata]
	if !ok {
		return fmt.Errorf("peer does not support %s", ExtMetadata)
	}

	m := map[string]int{
		"msg_type": 0, // Request
		"piece":    piece,
	}
	payloadBytes, err := bencode.EncodeBytes(m)
	if err != nil {
		return err
	}

	payload := append([]byte{byte(extID)}, payloadBytes...)
	return p.WriteMessage(&Message{
		ID:      MsgExtended,
		Payload: payload,
	})
}

// sendExtendedHandshake sends the BEP 10 handshake dictionary (msg ID 20, ext ID 0).
func (p *PeerConn) sendExtendedHandshake() error {
	m := map[string]interface{}{
		"m": map[string]int{
			ExtMetadata: 1, // We map ut_metadata to local ID 1
		},
	}
	payloadBytes, err := bencode.EncodeBytes(m)
	if err != nil {
		return err
	}

	// Extended message payload: <ext_id (1 byte)><bencoded dict>
	// Handshake ext_id is always 0.
	payload := append([]byte{0}, payloadBytes...)

	return WriteMessage(p.conn, &Message{
		ID:      MsgExtended,
		Payload: payload,
	})
}

// ReadMessage reads the next message from the peer.
func (p *PeerConn) ReadMessage() (*Message, error) {
	// Set a reasonable read timeout to avoid hanging forever
	p.conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
	return ReadMessage(p.conn)
}

// WriteMessage writes a message to the peer.
func (p *PeerConn) WriteMessage(m *Message) error {
	p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return WriteMessage(p.conn, m)
}

func (p *PeerConn) Close() {
	p.conn.Close()
}
