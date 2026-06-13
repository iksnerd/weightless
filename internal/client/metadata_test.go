package client

import (
	"bytes"
	"context"
	"crypto/sha1"
	"testing"

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
