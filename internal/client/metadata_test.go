package client

import (
	"bytes"
	"crypto/sha1"
	"testing"

	"github.com/zeebo/bencode"
)

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
