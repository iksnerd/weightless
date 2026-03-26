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
