package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"
	"time"

	"github.com/zeebo/bencode"
)

func TestHandshake(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer ln.Close()

	infoHash := make([]byte, 20)
	copy(infoHash, "infohash123456789012")
	peerID := make([]byte, 20)
	copy(peerID, "-WL0001-123456789012")

	done := make(chan bool)
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Read client handshake
		resBuf := make([]byte, 1)
		io.ReadFull(conn, resBuf) // pstrlen
		pstrlen := int(resBuf[0])
		payload := make([]byte, pstrlen+8+20+20)
		io.ReadFull(conn, payload)

		// Send peer handshake (same info hash)
		pstr := "BitTorrent protocol"
		buf := make([]byte, 1+len(pstr)+8+20+20)
		buf[0] = byte(len(pstr))
		copy(buf[1:], pstr)
		// Signal BEP 10 support in peer handshake
		buf[1+len(pstr)+5] |= 0x10
		copy(buf[1+len(pstr)+8:], infoHash)
		copy(buf[1+len(pstr)+8+20:], "-PEER01-123456789012")
		conn.Write(buf)

		// Check if client signaled BEP 10
		reserved := payload[pstrlen : pstrlen+8]
		if (reserved[5] & 0x10) != 0 {
			// 1. Read client's extended handshake
			ReadMessage(conn)

			// 2. Send peer's extended handshake
			m := map[string]interface{}{
				"m": map[string]int{"ut_metadata": 1},
			}
			data, _ := bencode.EncodeBytes(m)
			payload := append([]byte{0}, data...) // ext_id 0
			WriteMessage(conn, &Message{ID: MsgExtended, Payload: payload})
		}
	}()

	p, err := Connect(context.Background(), ln.Addr().String())
	if err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	defer p.Close()

	err = p.Handshake(context.Background(), infoHash, string(peerID))
	if err != nil {
		t.Fatalf("handshake failed: %v", err)
	}

	if p.PeerExtensions["ut_metadata"] != 1 {
		t.Errorf("expected ut_metadata=1 in extensions, got %v", p.PeerExtensions)
	}

	<-done
}

func TestHandshakeMismatch(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen failed: %v", err)
	}
	defer ln.Close()

	infoHash := make([]byte, 20)
	copy(infoHash, "correcthash123456789")

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()

		// Skip reading client's handshake
		resBuf := make([]byte, 1)
		io.ReadFull(conn, resBuf)
		pstrlen := int(resBuf[0])
		payload := make([]byte, pstrlen+8+20+20)
		io.ReadFull(conn, payload)

		// Send peer handshake with WRONG info hash
		pstr := "BitTorrent protocol"
		buf := make([]byte, 1+len(pstr)+8+20+20)
		buf[0] = byte(len(pstr))
		copy(buf[1:], pstr)
		copy(buf[1+len(pstr)+8:], []byte("wronghash12345678901")) // mismatch
		copy(buf[1+len(pstr)+8+20:], "-PEER01-123456789012")
		conn.Write(buf)
	}()

	p, err := Connect(context.Background(), ln.Addr().String())
	if err != nil {
		t.Fatalf("connect failed: %v", err)
	}
	defer p.Close()

	err = p.Handshake(context.Background(), infoHash, "-WL0001-123456789012")
	if err == nil {
		t.Fatal("expected handshake failure due to hash mismatch")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("info hash mismatch")) {
		t.Errorf("unexpected error: %v", err)
	}
}

// fakePeer accepts one connection, reads the client's handshake, and replies
// with a custom 68-byte handshake reply. Returns the listener's addr.
func fakePeer(t *testing.T, reply []byte) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		// Drain the client's 68-byte handshake.
		io.ReadFull(conn, make([]byte, 68))
		conn.Write(reply)
	}()
	return ln.Addr().String()
}

func TestHandshakeRejectsBadPstrlen(t *testing.T) {
	infoHash := make([]byte, 20)
	copy(infoHash, "infohash123456789012")

	// Reply claims pstrlen=18 instead of 19 — handshake reads exactly 68
	// bytes (1+19+8+20+20), so we need to construct a 68-byte buffer with
	// a bogus first byte. The parser should reject on the length check.
	reply := make([]byte, 68)
	reply[0] = 18                          // wrong
	copy(reply[1:], "BitTorrent protocol") // would be valid magic if length matched
	copy(reply[1+19+8:], infoHash)

	addr := fakePeer(t, reply)
	p, err := Connect(context.Background(), addr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer p.Close()

	err = p.Handshake(context.Background(), infoHash, "-WL0001-123456789012")
	if err == nil {
		t.Fatal("expected pstrlen rejection")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("pstrlen")) {
		t.Errorf("expected pstrlen error, got %v", err)
	}
}

func TestHandshakeRejectsBadMagic(t *testing.T) {
	infoHash := make([]byte, 20)
	copy(infoHash, "infohash123456789012")

	// pstrlen=19 (valid) but magic is wrong.
	reply := make([]byte, 68)
	reply[0] = 19
	copy(reply[1:], "WrongTorrent magic!") // 19 bytes, wrong content
	copy(reply[1+19+8:], infoHash)

	addr := fakePeer(t, reply)
	p, err := Connect(context.Background(), addr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer p.Close()

	err = p.Handshake(context.Background(), infoHash, "-WL0001-123456789012")
	if err == nil {
		t.Fatal("expected magic-string rejection")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("protocol magic")) {
		t.Errorf("expected protocol magic error, got %v", err)
	}
}

func TestReadMessageBeforeHandshakeRejected(t *testing.T) {
	// Set up a connection but never handshake.
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	p := &PeerConn{conn: client, state: stateInit}

	if _, err := p.ReadMessage(context.Background()); err == nil {
		t.Error("ReadMessage in stateInit should fail")
	}
	if err := p.WriteMessage(&Message{ID: 0}); err == nil {
		t.Error("WriteMessage in stateInit should fail")
	}
}

func TestReadMessageInterruptedByCancellation(t *testing.T) {
	// A peer that never sends anything would otherwise hold ReadMessage for
	// its full 2-minute steady-state timeout. Cancelling ctx must unblock it
	// immediately instead.
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	p := &PeerConn{conn: client, state: stateHandshook}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, err := p.ReadMessage(ctx)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected ReadMessage to return an error on cancellation")
	}
	if elapsed > 5*time.Second {
		t.Fatalf("ReadMessage took %v to return after cancellation, expected near-immediate", elapsed)
	}
	if ctx.Err() == nil {
		t.Fatal("expected ctx to be cancelled")
	}
}

func TestRequestMetadataBeforeExtendedRejected(t *testing.T) {
	server, client := net.Pipe()
	defer server.Close()
	defer client.Close()

	// Handshook but not Extended.
	p := &PeerConn{conn: client, state: stateHandshook}

	if err := p.RequestMetadata(0); err == nil {
		t.Error("RequestMetadata before extended handshake should fail")
	}
}

// extHandshakePeer accepts one connection, echoes the BEP 3 handshake with
// the BEP 10 bit set, reads the client's extended handshake, then writes
// each message in pre before finally sending the extended handshake dict
// (unless sendHandshake is false). It then keeps the conn open.
func extHandshakePeer(t *testing.T, pre []*Message, sendHandshake bool) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		buf := make([]byte, 68)
		if _, err := io.ReadFull(conn, buf); err != nil {
			return
		}
		conn.Write(buf) // echo: same info hash, BEP 10 bit preserved
		ReadMessage(conn)
		for _, m := range pre {
			if err := WriteMessage(conn, m); err != nil {
				return
			}
		}
		if sendHandshake {
			data, _ := bencode.EncodeBytes(map[string]interface{}{
				"m":             map[string]int{"ut_metadata": 1},
				"metadata_size": 321,
			})
			WriteMessage(conn, &Message{ID: MsgExtended, Payload: append([]byte{0}, data...)})
		}
		io.Copy(io.Discard, conn)
	}()
	return ln.Addr().String()
}

// Real clients (Transmission, libtorrent) send bitfield / have / keep-alive
// before their BEP 10 handshake. The handshake must tolerate that.
func TestHandshakeSkipsMessagesBeforeExtended(t *testing.T) {
	infoHash := make([]byte, 20)
	copy(infoHash, "infohash123456789012")

	pre := []*Message{
		{ID: MsgBitfield, Payload: []byte{0xff, 0x80}},
		KeepAlive,
		{ID: MsgHave, Payload: []byte{0, 0, 0, 3}},
		{ID: MsgUnchoke},
	}
	addr := extHandshakePeer(t, pre, true)
	p, err := Connect(context.Background(), addr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer p.Close()

	if err := p.Handshake(context.Background(), infoHash, "-WL0001-123456789012"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if p.PeerExtensions["ut_metadata"] != 1 {
		t.Errorf("PeerExtensions = %v, want ut_metadata=1", p.PeerExtensions)
	}
	if p.MetadataSize != 321 {
		t.Errorf("MetadataSize = %d, want 321", p.MetadataSize)
	}
	if p.PeerChoking {
		t.Error("Unchoke received before the extended handshake was not recorded")
	}
}

// A peer that floods ordinary messages and never sends the extended
// handshake is dropped after a bounded number of reads.
func TestHandshakeGivesUpWithoutExtended(t *testing.T) {
	infoHash := make([]byte, 20)
	copy(infoHash, "infohash123456789012")

	pre := make([]*Message, 0, maxPreExtHandshakeMsgs+4)
	for i := 0; i < maxPreExtHandshakeMsgs+4; i++ {
		pre = append(pre, &Message{ID: MsgHave, Payload: []byte{0, 0, 0, byte(i)}})
	}
	addr := extHandshakePeer(t, pre, true) // handshake comes too late to count
	p, err := Connect(context.Background(), addr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer p.Close()

	err = p.Handshake(context.Background(), infoHash, "-WL0001-123456789012")
	if err == nil {
		t.Fatal("expected handshake to give up")
	}
	if !bytes.Contains([]byte(err.Error()), []byte("no extended handshake within")) {
		t.Errorf("unexpected error: %v", err)
	}
}

// A peer that completes the BEP 3 handshake but then goes silent must not
// hold the connection past the handshake deadline: the ctx deadline bounds
// the extended-handshake phase too, not just the first 68 bytes.
func TestHandshakeHonoursDeadlineWaitingForExtended(t *testing.T) {
	infoHash := make([]byte, 20)
	copy(infoHash, "infohash123456789012")

	addr := extHandshakePeer(t, nil, false) // echoes handshake, then silence
	p, err := Connect(context.Background(), addr)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer p.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	start := time.Now()
	err = p.Handshake(ctx, infoHash, "-WL0001-123456789012")
	if err == nil {
		t.Fatal("expected handshake to time out waiting for extended handshake")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("handshake blocked %v; the ctx deadline (500ms) should have bounded it", elapsed)
	}
}
