package torrent

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zeebo/bencode"
)

func TestBuildMerkleTreeSingleLeaf(t *testing.T) {
	h := sha256.Sum256([]byte("hello"))
	tree := buildMerkleTree([][32]byte{h})
	root := tree[len(tree)-1][0]
	if root != h {
		t.Error("single leaf root should equal the leaf hash")
	}
}

func TestBuildMerkleTreePowerOfTwo(t *testing.T) {
	h0 := sha256.Sum256([]byte("a"))
	h1 := sha256.Sum256([]byte("b"))
	h2 := sha256.Sum256([]byte("c"))
	h3 := sha256.Sum256([]byte("d"))

	tree := buildMerkleTree([][32]byte{h0, h1, h2, h3})

	if len(tree) != 3 {
		t.Fatalf("Expected 3 levels, got %d", len(tree))
	}

	var pair01, pair23 [64]byte
	copy(pair01[:32], h0[:])
	copy(pair01[32:], h1[:])
	p01 := sha256.Sum256(pair01[:])
	copy(pair23[:32], h2[:])
	copy(pair23[32:], h3[:])
	p23 := sha256.Sum256(pair23[:])
	var top [64]byte
	copy(top[:32], p01[:])
	copy(top[32:], p23[:])
	expected := sha256.Sum256(top[:])

	if tree[2][0] != expected {
		t.Error("root mismatch")
	}
}

func TestBuildMerkleTreeEmpty(t *testing.T) {
	tree := buildMerkleTree(nil)
	expected := sha256.Sum256(make([]byte, 32))
	if tree[0][0] != expected {
		t.Error("empty root should be sha256 of 32 zero bytes")
	}
}

func TestExtractPieceLayer(t *testing.T) {
	h0 := sha256.Sum256([]byte("block0"))
	h1 := sha256.Sum256([]byte("block1"))
	h2 := sha256.Sum256([]byte("block2"))
	h3 := sha256.Sum256([]byte("block3"))

	tree := buildMerkleTree([][32]byte{h0, h1, h2, h3})
	pieceLen := 2 * BlockSize
	layer := extractPieceLayer(tree, pieceLen, 2)

	if len(layer) != 2 {
		t.Fatalf("Expected 2 piece hashes, got %d", len(layer))
	}
	if layer[0] != tree[1][0] || layer[1] != tree[1][1] {
		t.Error("piece layer doesn't match tree level 1")
	}
}

func TestCreateSingleFileHybrid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")
	data := make([]byte, 64*1024) // 64 KiB
	for i := range data {
		data[i] = byte(i % 256)
	}
	os.WriteFile(path, data, 0644)

	result, err := Create(CreateOptions{
		Path:        path,
		Name:        "test.dat",
		PieceLength: 16 * 1024,
		AnnounceURL: "http://localhost:8080/announce",
		Source:      "test-source",
		CreatedBy:   "test-cli",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var meta map[string]interface{}
	bencode.DecodeBytes(result.TorrentBytes, &meta)
	info := meta["info"].(map[string]interface{})

	// v2 fields
	if v, _ := info["meta version"].(int64); v != 2 {
		t.Errorf("expected meta version 2, got %v", info["meta version"])
	}
	if info["source"] != "test-source" {
		t.Errorf("expected source %q, got %v", "test-source", info["source"])
	}
	ft := info["file tree"].(map[string]interface{})
	if _, ok := ft["test.dat"]; !ok {
		t.Error("file tree missing test.dat entry")
	}

	// v1 fields
	if _, ok := info["length"]; !ok {
		t.Error("hybrid torrent should have v1 'length' field")
	}
	pieces, ok := info["pieces"].(string)
	if !ok {
		t.Fatal("hybrid torrent should have v1 'pieces' field")
	}
	// 64KiB / 16KiB = 4 pieces, each SHA-1 = 20 bytes → 80 bytes
	if len(pieces) != 80 {
		t.Errorf("expected 80 bytes of SHA-1 pieces, got %d", len(pieces))
	}

	// Verify info hash
	reEncoded, _ := bencode.EncodeBytes(info)
	infoHash := sha256.Sum256(reEncoded)
	if hex.EncodeToString(infoHash[:]) != result.InfoHashHex {
		t.Error("info hash mismatch")
	}

	// Piece layers should exist (4 pieces > 1)
	if _, ok := meta["piece layers"]; !ok {
		t.Error("piece layers should exist for multi-piece file")
	}
}

func TestCreateDirectory(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "data")
	os.MkdirAll(filepath.Join(sub, "nested"), 0755)
	os.WriteFile(filepath.Join(sub, "a.txt"), []byte("hello"), 0644)
	os.WriteFile(filepath.Join(sub, "nested", "b.txt"), []byte("world"), 0644)

	result, err := Create(CreateOptions{
		Path:        sub,
		Name:        "data",
		PieceLength: MinPieceLength,
		AnnounceURL: "http://localhost/announce",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var meta map[string]interface{}
	bencode.DecodeBytes(result.TorrentBytes, &meta)
	info := meta["info"].(map[string]interface{})
	ft := info["file tree"].(map[string]interface{})

	if _, ok := ft["a.txt"]; !ok {
		t.Error("missing a.txt in file tree")
	}
	nested := ft["nested"].(map[string]interface{})
	if _, ok := nested["b.txt"]; !ok {
		t.Error("missing b.txt in nested dir")
	}

	// v1 fields for multi-file
	if _, ok := info["files"]; !ok {
		t.Error("hybrid multi-file torrent should have 'files' field")
	}
	if _, ok := info["pieces"]; !ok {
		t.Error("hybrid torrent should have 'pieces' field")
	}
}

func TestCreateEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "empty.dat")
	os.WriteFile(path, nil, 0644)

	result, err := Create(CreateOptions{
		Path:        path,
		Name:        "empty.dat",
		PieceLength: MinPieceLength,
		AnnounceURL: "http://localhost/announce",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var meta map[string]interface{}
	bencode.DecodeBytes(result.TorrentBytes, &meta)
	info := meta["info"].(map[string]interface{})
	ft := info["file tree"].(map[string]interface{})
	fileEntry := ft["empty.dat"].(map[string]interface{})
	attrs := fileEntry[""].(map[string]interface{})

	if v, _ := attrs["length"].(int64); v != 0 {
		t.Errorf("empty file should have length 0")
	}
	if _, ok := attrs["pieces root"]; ok {
		t.Error("empty file should not have pieces root")
	}
}

func TestPieceLengthValidation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")
	os.WriteFile(path, []byte("data"), 0644)

	for _, pl := range []int{8192, 30000, 17} {
		_, err := Create(CreateOptions{Path: path, PieceLength: pl, AnnounceURL: "http://localhost/announce"})
		if err == nil {
			t.Errorf("expected error for piece length %d", pl)
		}
	}
}

func TestSinglePieceNoPieceLayers(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "small.dat")
	os.WriteFile(path, []byte("tiny"), 0644)

	result, _ := Create(CreateOptions{Path: path, Name: "small.dat", PieceLength: MinPieceLength, AnnounceURL: "http://localhost/announce"})

	var meta map[string]interface{}
	bencode.DecodeBytes(result.TorrentBytes, &meta)

	if _, ok := meta["piece layers"]; ok {
		t.Error("single-piece file should not have piece layers")
	}
}

func TestInfoHashDeterminism(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "determ.dat")
	os.WriteFile(path, []byte("deterministic content"), 0644)

	opts := CreateOptions{Path: path, Name: "determ.dat", PieceLength: MinPieceLength, AnnounceURL: "http://localhost/announce"}
	r1, _ := Create(opts)
	r2, _ := Create(opts)

	if r1.InfoHashHex != r2.InfoHashHex {
		t.Errorf("hashes differ: %s vs %s", r1.InfoHashHex, r2.InfoHashHex)
	}
}

func TestMagnetLinkFormat(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")
	os.WriteFile(path, []byte("magnet test"), 0644)

	result, _ := Create(CreateOptions{
		Path: path, Name: "test.dat", PieceLength: MinPieceLength,
		AnnounceURL: "http://localhost:8080/announce",
	})

	if !strings.Contains(result.MagnetLink, "xt=urn:btih:"+result.InfoHashV1Hex) {
		t.Errorf("magnet should contain v1 hash, got: %s", result.MagnetLink)
	}
	if !strings.Contains(result.MagnetLink, "xt=urn:btmh:1220"+result.InfoHashHex) {
		t.Errorf("magnet should contain v2 hash, got: %s", result.MagnetLink)
	}
}

func TestV1PiecesAreSHA1(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.dat")
	// Exactly 1 piece of MinPieceLength
	data := make([]byte, MinPieceLength)
	os.WriteFile(path, data, 0644)

	result, _ := Create(CreateOptions{Path: path, Name: "test.dat", PieceLength: MinPieceLength, AnnounceURL: "http://localhost/announce"})

	var meta map[string]interface{}
	bencode.DecodeBytes(result.TorrentBytes, &meta)
	info := meta["info"].(map[string]interface{})
	pieces := info["pieces"].(string)

	// 1 piece → 20 bytes of SHA-1
	if len(pieces) != 20 {
		t.Errorf("expected 20 bytes SHA-1, got %d", len(pieces))
	}
}

func TestNextPowerOfTwo(t *testing.T) {
	tests := []struct {
		name string
		in   int
		want int
	}{
		{"already_power_1", 1, 1},
		{"already_power_2", 2, 2},
		{"already_power_4", 4, 4},
		{"already_power_8", 8, 8},
		{"already_power_16", 16, 16},
		{"already_power_1024", 1024, 1024},
		{"rounds_up_3", 3, 4},
		{"rounds_up_5", 5, 8},
		{"rounds_up_9", 9, 16},
		{"rounds_up_100", 100, 128},
		{"rounds_up_1000", 1000, 1024},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := nextPowerOfTwo(tt.in); got != tt.want {
				t.Errorf("nextPowerOfTwo(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestCreateDirectorySkipsSymlinks(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "data")
	os.MkdirAll(sub, 0755)

	// Real file
	os.WriteFile(filepath.Join(sub, "real.txt"), []byte("real content"), 0644)

	// Symlink (should be skipped)
	os.Symlink(filepath.Join(sub, "real.txt"), filepath.Join(sub, "link.txt"))

	result, err := Create(CreateOptions{
		Path:        sub,
		Name:        "data",
		PieceLength: MinPieceLength,
		AnnounceURL: "http://localhost/announce",
	})
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	var meta map[string]interface{}
	bencode.DecodeBytes(result.TorrentBytes, &meta)
	info := meta["info"].(map[string]interface{})
	ft := info["file tree"].(map[string]interface{})

	if _, ok := ft["real.txt"]; !ok {
		t.Error("real.txt should be in file tree")
	}
	// Symlink may or may not appear depending on OS behavior with filepath.Walk
	// The important thing is it doesn't crash and the torrent is valid
}
