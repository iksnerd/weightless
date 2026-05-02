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

// BEP 3 wire constants. The handshake is *exactly* 68 bytes: 1 length byte +
// 19 magic bytes + 8 reserved + 20 info_hash + 20 peer_id. Anything else is
// off-spec and rejected.
const (
	protoMagic   = "BitTorrent protocol"
	pstrLen      = 19 // len(protoMagic)
	handshakeLen = 1 + pstrLen + 8 + 20 + 20
)

// peerState is the LangSec-style typed state of a PeerConn. Methods that
// operate on a connection require a minimum state and refuse to run otherwise.
type peerState int

const (
	stateInit      peerState = iota // freshly TCP-connected, no handshake yet
	stateHandshook                  // BEP 3 handshake complete
	stateExtended                   // BEP 10 extended handshake complete
)

// PeerConn wraps a TCP connection to a peer and tracks PWP state.
type PeerConn struct {
	conn  net.Conn
	addr  string
	state peerState

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
// LangSec recognition: the peer's reply must be exactly 68 bytes,
// pstrlen must be exactly 19, and pstr must be exactly "BitTorrent protocol".
// Anything else is off-spec and the connection is dropped before any state
// (PeerExtensions, MetadataSize, choke/interest flags) is touched.
//
// NOTE: Even for hybrid v1+v2 torrents, the BitTorrent wire protocol
// handshake ALWAYS uses the 20-byte SHA-1 info hash (v1) per BEP 3.
func (p *PeerConn) Handshake(ctx context.Context, infoHash []byte, peerID string) error {
	if p.state != stateInit {
		return fmt.Errorf("handshake called in state %d (expected init)", p.state)
	}
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

	buf := make([]byte, handshakeLen)
	buf[0] = byte(pstrLen)
	curr := 1
	curr += copy(buf[curr:], protoMagic)

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

	// Read the peer's full 68-byte handshake. LangSec: read it whole, then
	// validate the entire structure before letting anything downstream touch it.
	resBuf := make([]byte, handshakeLen)
	if _, err := io.ReadFull(p.conn, resBuf); err != nil {
		return fmt.Errorf("read handshake: %w", err)
	}

	if int(resBuf[0]) != pstrLen {
		return fmt.Errorf("invalid pstrlen %d (must be %d per BEP 3)", resBuf[0], pstrLen)
	}
	if !bytes.Equal(resBuf[1:1+pstrLen], []byte(protoMagic)) {
		return fmt.Errorf("invalid protocol magic: got %q", resBuf[1:1+pstrLen])
	}

	resInfoHash := resBuf[1+pstrLen+8 : 1+pstrLen+8+20]
	if !bytes.Equal(resInfoHash, infoHash) {
		return fmt.Errorf("info hash mismatch: expected %x, got %x", infoHash, resInfoHash)
	}

	// All recognition passed — promote state.
	p.state = stateHandshook

	// Check if peer supports BEP 10
	peerReserved := resBuf[1+pstrLen : 1+pstrLen+8]
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
		p.state = stateExtended
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
// Requires that the BEP 10 extended handshake has completed.
func (p *PeerConn) RequestMetadata(piece int) error {
	if p.state < stateExtended {
		return fmt.Errorf("RequestMetadata called in state %d (expected extended)", p.state)
	}
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

// ReadMessage reads the next message from the peer. Requires the BEP 3
// handshake to have completed — until then, raw bytes on the wire don't
// frame as PWP messages.
func (p *PeerConn) ReadMessage() (*Message, error) {
	if p.state < stateHandshook {
		return nil, fmt.Errorf("ReadMessage called in state %d (expected handshook)", p.state)
	}
	// Set a reasonable read timeout to avoid hanging forever
	p.conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
	return ReadMessage(p.conn)
}

// WriteMessage writes a message to the peer. Requires the BEP 3 handshake
// to have completed.
func (p *PeerConn) WriteMessage(m *Message) error {
	if p.state < stateHandshook {
		return fmt.Errorf("WriteMessage called in state %d (expected handshook)", p.state)
	}
	p.conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return WriteMessage(p.conn, m)
}

func (p *PeerConn) Close() {
	p.conn.Close()
}
