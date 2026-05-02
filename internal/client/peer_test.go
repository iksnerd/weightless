package client

import (
	"bytes"
	"context"
	"io"
	"net"
	"testing"

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

	if _, err := p.ReadMessage(); err == nil {
		t.Error("ReadMessage in stateInit should fail")
	}
	if err := p.WriteMessage(&Message{ID: 0}); err == nil {
		t.Error("WriteMessage in stateInit should fail")
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
