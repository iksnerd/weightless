package client

import (
	"bytes"
	"context"
	"crypto/sha1"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/zeebo/bencode"
)

func TestFetchMetadataEndToEnd(t *testing.T) {
	t.Parallel()
	// Arbitrary bytes — FetchMetadata only SHA-1-verifies, it doesn't parse.
	// Use >16 KiB to exercise multi-piece assembly.
	metaBytes := make([]byte, 40000)
	for i := range metaBytes {
		metaBytes[i] = byte(i * 7)
	}
	infoHash := sha1.Sum(metaBytes)

	ln, err := listenTCP(t)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err == nil {
			serveMetadataPeer(c, metaBytes)
		}
	}()

	ctx := context.Background()
	p, err := Connect(ctx, ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Handshake(ctx, infoHash[:], "-WL0020-abcdef012345"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	if p.MetadataSize != len(metaBytes) {
		t.Fatalf("MetadataSize = %d, want %d", p.MetadataSize, len(metaBytes))
	}
	got, err := p.FetchMetadata(ctx, infoHash[:])
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if !bytes.Equal(got, metaBytes) {
		t.Errorf("fetched metadata mismatch: got %d bytes, want %d", len(got), len(metaBytes))
	}
}

func TestFetchMetadataRejectsBadSize(t *testing.T) {
	t.Parallel()
	// The size guards run before any network I/O, so a bare PeerConn is enough.
	tests := []struct {
		name string
		size int
	}{
		{"zero", 0},
		{"negative", -1},
		{"too large", maxMetadataSize + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			p := &PeerConn{MetadataSize: tt.size}
			if _, err := p.FetchMetadata(context.Background(), make([]byte, 20)); err == nil {
				t.Fatalf("FetchMetadata(size=%d) = nil error, want rejection", tt.size)
			}
		})
	}
}

func TestFindBencodeEnd(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		input []byte
		want  int
	}{
		{"dict", []byte("d3:foo3:bare"), 12},
		{"dict with int", []byte("d3:fooi42ee"), 11},
		{"empty dict", []byte("de"), 2},
		{"nested dict", []byte("d1:ad1:bi1eee"), 13},
		{"string", []byte("4:test"), 6},
		{"int", []byte("i42e"), 4},
		{"list", []byte("l3:fooi1ee"), 10},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := findBencodeEnd(tt.input)
			if got != tt.want {
				t.Errorf("findBencodeEnd(%q) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestFindBencodeEndWithTrailingData(t *testing.T) {
	t.Parallel()
	// Dict followed by raw binary data (the BEP 9 case)
	dict := []byte("d8:msg_typei1e5:piecei0e10:total_sizei1024ee")
	trailing := []byte("raw piece data here")
	input := append(dict, trailing...)

	end := findBencodeEnd(input)
	if end != len(dict) {
		t.Errorf("findBencodeEnd = %d, want %d", end, len(dict))
	}

	// Data after the dict
	remaining := input[end:]
	if !bytes.Equal(remaining, trailing) {
		t.Errorf("remaining = %q, want %q", remaining, trailing)
	}
}

func TestBEP9PayloadParsing(t *testing.T) {
	t.Parallel()
	// Simulate a full BEP 9 data response: <ext_id><bencoded header><raw data>
	rawData := []byte("this is the metadata piece content!!")

	header := map[string]int{
		"msg_type":   1, // Data
		"piece":      0,
		"total_size": len(rawData),
	}
	headerBytes, err := bencode.EncodeBytes(header)
	if err != nil {
		t.Fatal(err)
	}

	// Build the full payload: ext_id (1 byte) + bencoded header + raw data
	var payload []byte
	payload = append(payload, 1) // ext_id
	payload = append(payload, headerBytes...)
	payload = append(payload, rawData...)

	// Parse using findBencodeEnd (same logic as metadata.go)
	dictEnd := findBencodeEnd(payload[1:])
	if dictEnd < 0 {
		t.Fatal("could not find end of bencoded header")
	}

	pieceData := payload[1+dictEnd:]
	if !bytes.Equal(pieceData, rawData) {
		t.Errorf("piece data = %q, want %q", pieceData, rawData)
	}
}

func TestBEP9MetadataHashVerification(t *testing.T) {
	t.Parallel()
	metadata := []byte("test metadata content for hashing")
	hash := sha1.Sum(metadata)

	// Same content should produce same hash
	hash2 := sha1.Sum(metadata)
	if hash != hash2 {
		t.Error("same content should produce same hash")
	}

	// Different content should produce different hash
	other := sha1.Sum([]byte("different content"))
	if hash == other {
		t.Error("different content should produce different hashes")
	}
}

// A real peer interleaves PEX, keep-alives and have/bitfield with its
// ut_metadata replies. FetchMetadata must skip past all of that, and must
// not drop the PEX peers it skipped over.
func TestFetchMetadataSkipsInterleavedMessages(t *testing.T) {
	t.Parallel()
	metaBytes := make([]byte, 40000) // 3 pieces: 16384, 16384, 7232
	for i := range metaBytes {
		metaBytes[i] = byte(i * 13)
	}
	infoHash := sha1.Sum(metaBytes)
	const pexAddr = "10.1.2.3:51413"

	ln, err := listenTCP(t)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		serveMetadataPeerWith(c, metaBytes, func(conn net.Conn, piece int) {
			WriteMessage(conn, KeepAlive)
			WriteMessage(conn, &Message{ID: MsgHave, Payload: []byte{0, 0, 0, 0}})
			WriteMessage(conn, &Message{ID: MsgExtended, Payload: pexMessagePayload(t, pexAddr)})
			// An extended message for an extension we never advertised.
			WriteMessage(conn, &Message{ID: MsgExtended, Payload: []byte{9, 'd', 'e'}})
		})
	}()

	ctx := context.Background()
	p, err := Connect(ctx, ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if err := p.Handshake(ctx, infoHash[:], "-WL0020-abcdef012345"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	got, err := p.FetchMetadata(ctx, infoHash[:])
	if err != nil {
		t.Fatalf("FetchMetadata: %v", err)
	}
	if !bytes.Equal(got, metaBytes) {
		t.Errorf("fetched metadata mismatch: got %d bytes, want %d", len(got), len(metaBytes))
	}

	pex := p.DrainPexPeers()
	if len(pex) != 3 || pex[0] != pexAddr {
		t.Errorf("PEX peers lost while skipping: got %v, want 3 x %s", pex, pexAddr)
	}
}

// metadataPeerReplying builds a fake ut_metadata peer whose data reply for
// every request is produced by reply(piece) — used to inject off-spec
// replies (wrong piece index, wrong length) and assert rejection.
func metadataPeerReplying(t *testing.T, metaSize int, reply func(piece int) []byte) string {
	t.Helper()
	ln, err := listenTCP(t)
	if err != nil {
		t.Fatal(err)
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
		conn.Write(buf)
		ReadMessage(conn) // client's extended handshake
		ext, _ := bencode.EncodeBytes(map[string]interface{}{
			"m":             map[string]int{"ut_metadata": 1},
			"metadata_size": metaSize,
		})
		WriteMessage(conn, &Message{ID: MsgExtended, Payload: append([]byte{0}, ext...)})
		for {
			m, err := ReadMessage(conn)
			if err != nil {
				return
			}
			if m == KeepAlive || m.ID != MsgExtended || len(m.Payload) < 2 {
				continue
			}
			var req struct {
				MsgType int `bencode:"msg_type"`
				Piece   int `bencode:"piece"`
			}
			if err := bencode.DecodeBytes(m.Payload[1:], &req); err != nil || req.MsgType != 0 {
				continue
			}
			WriteMessage(conn, &Message{ID: MsgExtended, Payload: reply(req.Piece)})
		}
	}()
	return ln.Addr().String()
}

func metadataDataReply(piece, total int, data []byte) []byte {
	header, _ := bencode.EncodeBytes(map[string]int{"msg_type": 1, "piece": piece, "total_size": total})
	payload := append([]byte{localMetadataID}, header...)
	return append(payload, data...)
}

func TestFetchMetadataRejectsOffSpecReplies(t *testing.T) {
	t.Parallel()
	const metaSize = 20000 // 2 pieces: 16384 + 3616
	tests := []struct {
		name    string
		reply   func(piece int) []byte
		wantErr string
	}{
		{
			name: "wrong piece index",
			reply: func(piece int) []byte {
				return metadataDataReply(piece+1, metaSize, make([]byte, 16384))
			},
			wantErr: "piece mismatch",
		},
		{
			name: "short piece",
			reply: func(piece int) []byte {
				return metadataDataReply(piece, metaSize, make([]byte, 100))
			},
			wantErr: "got 100 bytes, want 16384",
		},
		{
			name: "oversized last piece",
			reply: func(piece int) []byte {
				if piece == 0 {
					return metadataDataReply(0, metaSize, make([]byte, 16384))
				}
				return metadataDataReply(piece, metaSize, make([]byte, 16384))
			},
			wantErr: "got 16384 bytes, want 3616",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			addr := metadataPeerReplying(t, metaSize, tt.reply)
			ctx := context.Background()
			p, err := Connect(ctx, addr)
			if err != nil {
				t.Fatal(err)
			}
			defer p.Close()
			infoHash := make([]byte, 20)
			if err := p.Handshake(ctx, infoHash, "-WL0020-abcdef012345"); err != nil {
				t.Fatalf("handshake: %v", err)
			}
			_, err = p.FetchMetadata(ctx, infoHash)
			if err == nil {
				t.Fatal("FetchMetadata accepted an off-spec reply")
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// A peer that never answers the metadata request (only noise) must be given
// up on after a bounded number of messages, not read forever.
func TestFetchMetadataGivesUpOnEndlessNoise(t *testing.T) {
	t.Parallel()
	ln, err := listenTCP(t)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
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
		conn.Write(buf)
		ReadMessage(conn)
		ext, _ := bencode.EncodeBytes(map[string]interface{}{
			"m":             map[string]int{"ut_metadata": 1},
			"metadata_size": 100,
		})
		WriteMessage(conn, &Message{ID: MsgExtended, Payload: append([]byte{0}, ext...)})
		ReadMessage(conn) // the metadata request
		for i := 0; i < maxMetadataSkipMsgs+8; i++ {
			if err := WriteMessage(conn, &Message{ID: MsgHave, Payload: []byte{0, 0, 0, 0}}); err != nil {
				return
			}
		}
		// Keep the conn open so a non-terminating loop would hang the test.
		io.Copy(io.Discard, conn)
	}()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	p, err := Connect(ctx, ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	infoHash := make([]byte, 20)
	if err := p.Handshake(ctx, infoHash, "-WL0020-abcdef012345"); err != nil {
		t.Fatalf("handshake: %v", err)
	}
	_, err = p.FetchMetadata(ctx, infoHash)
	if err == nil || !strings.Contains(err.Error(), "no ut_metadata reply") {
		t.Fatalf("err = %v, want bounded give-up", err)
	}
}
