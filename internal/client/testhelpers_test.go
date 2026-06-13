package client

import (
	"encoding/binary"
	"io"
	"net"
	"testing"

	"github.com/zeebo/bencode"
)

func listenTCP(t *testing.T) (net.Listener, error) {
	t.Helper()
	return net.Listen("tcp", "127.0.0.1:0")
}

// serveMetadataPeer simulates a peer that supports BEP 9 ut_metadata: it
// advertises metadata_size in the extended handshake and answers metadata
// requests with the bytes of metaBytes in 16 KiB pieces.
func serveMetadataPeer(conn net.Conn, metaBytes []byte) {
	defer conn.Close()

	buf := make([]byte, 68)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}
	conn.Write(buf) // echo handshake (preserves BEP 10 bit + info hash)
	if buf[25]&0x10 == 0 {
		return
	}
	ReadMessage(conn) // client's extended handshake

	extPayload, _ := bencode.EncodeBytes(map[string]interface{}{
		"m":             map[string]int{"ut_metadata": 1},
		"metadata_size": len(metaBytes),
	})
	WriteMessage(conn, &Message{ID: MsgExtended, Payload: append([]byte{0}, extPayload...)})

	for {
		m, err := ReadMessage(conn)
		if err != nil {
			return
		}
		if m == nil || m.ID != MsgExtended || len(m.Payload) < 2 {
			continue
		}
		var req struct {
			MsgType int `bencode:"msg_type"`
			Piece   int `bencode:"piece"`
		}
		if err := bencode.DecodeBytes(m.Payload[1:], &req); err != nil || req.MsgType != 0 {
			continue
		}
		start := req.Piece * 16384
		end := start + 16384
		if start > len(metaBytes) {
			start = len(metaBytes)
		}
		if end > len(metaBytes) {
			end = len(metaBytes)
		}
		header, _ := bencode.EncodeBytes(map[string]int{
			"msg_type":   1, // Data
			"piece":      req.Piece,
			"total_size": len(metaBytes),
		})
		payload := append([]byte{1}, header...) // ext id (arbitrary) + header
		payload = append(payload, metaBytes[start:end]...)
		WriteMessage(conn, &Message{ID: MsgExtended, Payload: payload})
	}
}

// pexMessagePayload builds the body of a ut_pex (BEP 11) extended message
// advertising a single IPv4 peer. The payload is addressed with localPexID —
// the extension ID our client advertises — exactly as a real peer would send it.
func pexMessagePayload(t *testing.T, addr string) []byte {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split %q: %v", addr, err)
	}
	ip := net.ParseIP(host).To4()
	if ip == nil {
		t.Fatalf("not an IPv4 addr: %q", addr)
	}
	port, err := net.LookupPort("tcp", portStr)
	if err != nil {
		t.Fatalf("port %q: %v", portStr, err)
	}
	added := append([]byte{}, ip...)
	added = append(added, byte(port>>8), byte(port))

	dict, _ := bencode.EncodeBytes(struct {
		Added []byte `bencode:"added"`
	}{added})
	return append([]byte{localPexID}, dict...)
}

// servePexThenChokePeer serves piece data, but: (1) right after unchoking it
// sends one ut_pex message advertising pexAddr, and (2) it refuses to serve the
// piece at chokeIndex by replying with a Choke. This forces the swarm to
// discover the PEX-advertised peer and use it to finish the choked piece.
func servePexThenChokePeer(t *testing.T, conn net.Conn, allData []byte, pexAddr string, chokeIndex uint32) {
	defer conn.Close()

	buf := make([]byte, 68)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}
	conn.Write(buf) // echo handshake
	if buf[25]&0x10 != 0 {
		ReadMessage(conn) // client's extended handshake
		extPayload, _ := bencode.EncodeBytes(map[string]interface{}{
			"m": map[string]int{"ut_metadata": 1, "ut_pex": 2},
		})
		WriteMessage(conn, &Message{ID: MsgExtended, Payload: append([]byte{0}, extPayload...)})
	}

	pexSent := false
	for {
		m, err := ReadMessage(conn)
		if err != nil || m == nil {
			return
		}
		switch m.ID {
		case MsgInterested:
			WriteMessage(conn, &Message{ID: MsgUnchoke})
			if !pexSent {
				WriteMessage(conn, &Message{ID: MsgExtended, Payload: pexMessagePayload(t, pexAddr)})
				pexSent = true
			}
		case MsgRequest:
			if len(m.Payload) < 12 {
				return
			}
			index := binary.BigEndian.Uint32(m.Payload[0:4])
			begin := binary.BigEndian.Uint32(m.Payload[4:8])
			length := binary.BigEndian.Uint32(m.Payload[8:12])
			if index == chokeIndex {
				WriteMessage(conn, &Message{ID: MsgChoke})
				continue
			}
			globalOffset := int(index)*16 + int(begin)
			end := globalOffset + int(length)
			if end > len(allData) {
				end = len(allData)
			}
			payload := make([]byte, 8+end-globalOffset)
			binary.BigEndian.PutUint32(payload[0:4], index)
			binary.BigEndian.PutUint32(payload[4:8], begin)
			copy(payload[8:], allData[globalOffset:end])
			WriteMessage(conn, &Message{ID: MsgPiece, Payload: payload})
		}
	}
}

// handleTestPeerConn simulates a minimal BitTorrent peer.
// It handles BEP 3 handshake (echoed), BEP 10 extended handshake, and piece requests.
func handleTestPeerConn(conn net.Conn, allData []byte) {
	defer conn.Close()

	// 1. Read client handshake (68 bytes for standard protocol)
	buf := make([]byte, 68)
	if _, err := io.ReadFull(conn, buf); err != nil {
		return
	}
	// Echo handshake back (preserves BEP 10 bit)
	conn.Write(buf)

	// 2. BEP 10 extended handshake
	// Check if client signaled BEP 10 (byte 25 = reserved[5], bit 0x10)
	if buf[25]&0x10 != 0 {
		// Read client's extended handshake
		ReadMessage(conn)

		// Send our extended handshake
		extPayload, _ := bencode.EncodeBytes(map[string]interface{}{
			"m": map[string]int{"ut_metadata": 1},
		})
		WriteMessage(conn, &Message{
			ID:      MsgExtended,
			Payload: append([]byte{0}, extPayload...),
		})
	}

	// 3. Message loop
	for {
		m, err := ReadMessage(conn)
		if err != nil {
			return
		}
		if m == nil {
			continue
		}

		switch m.ID {
		case MsgInterested:
			WriteMessage(conn, &Message{ID: MsgUnchoke})

		case MsgRequest:
			if len(m.Payload) < 12 {
				return
			}
			index := binary.BigEndian.Uint32(m.Payload[0:4])
			begin := binary.BigEndian.Uint32(m.Payload[4:8])
			length := binary.BigEndian.Uint32(m.Payload[8:12])

			// Calculate global offset
			// For simplicity, assume pieceLength = 16 (test default)
			// The caller must ensure allData has the right layout
			globalOffset := int(index)*16 + int(begin)
			end := globalOffset + int(length)
			if end > len(allData) {
				end = len(allData)
			}

			payload := make([]byte, 8+end-globalOffset)
			binary.BigEndian.PutUint32(payload[0:4], index)
			binary.BigEndian.PutUint32(payload[4:8], begin)
			copy(payload[8:], allData[globalOffset:end])
			WriteMessage(conn, &Message{ID: MsgPiece, Payload: payload})

		case MsgExtended:
			// Ignore
		}
	}
}
