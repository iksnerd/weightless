package torrent

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zeebo/bencode"
)

func TestParse(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test_parse")
	if err := os.WriteFile(path, []byte("test parse content"), 0644); err != nil {
		t.Fatal(err)
	}

	result, err := Create(CreateOptions{
		Path:        path,
		Name:        "test_parse",
		PieceLength: MinPieceLength,
		AnnounceURL: "http://localhost:8080/announce",
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	meta, err := Parse(result.TorrentBytes)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if meta.Name != "test_parse" {
		t.Errorf("expected name test_parse, got %s", meta.Name)
	}
	if meta.TotalSize != 18 {
		t.Errorf("expected size 18, got %d", meta.TotalSize)
	}
	if len(meta.Files) != 1 {
		t.Errorf("expected 1 file, got %d", len(meta.Files))
	}
	if meta.Files[0].Path != "test_parse" {
		t.Errorf("expected path test_parse, got %s", meta.Files[0].Path)
	}
	if meta.PieceLength != MinPieceLength {
		t.Errorf("expected piece length %d, got %d", MinPieceLength, meta.PieceLength)
	}
	if meta.PieceCount != 1 {
		t.Errorf("expected 1 piece, got %d", meta.PieceCount)
	}
}

func TestParseDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "dataset")
	os.MkdirAll(dir, 0755)
	os.WriteFile(filepath.Join(dir, "a.txt"), []byte("aaaa"), 0644)
	os.WriteFile(filepath.Join(dir, "b.txt"), []byte("bbbb"), 0644)

	result, err := Create(CreateOptions{
		Path:        dir,
		Name:        "dataset",
		PieceLength: MinPieceLength,
		AnnounceURL: "http://localhost:8080/announce",
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	meta, err := Parse(result.TorrentBytes)
	if err != nil {
		t.Fatalf("parse failed: %v", err)
	}

	if meta.Name != "dataset" {
		t.Errorf("expected name dataset, got %s", meta.Name)
	}
	if meta.TotalSize != 8 {
		t.Errorf("expected size 8, got %d", meta.TotalSize)
	}
	if len(meta.Files) != 2 {
		t.Errorf("expected 2 files, got %d", len(meta.Files))
	}
}

func TestParseRejectsMalformedBencode(t *testing.T) {
	cases := []struct {
		name string
		data []byte
	}{
		{"empty", []byte{}},
		{"trailing garbage", []byte("d3:foo3:baree!!")},
		{"unterminated dict", []byte("d3:foo3:bar")},
		{"non-bencode", []byte("hello world")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if _, err := Parse(c.data); err == nil {
				t.Errorf("expected error for %s, got nil", c.name)
			}
		})
	}
}

func TestParseRejectsDeeplyNested(t *testing.T) {
	// Bencode validator caps depth at 64; build 70-deep nested list.
	data := []byte(strings.Repeat("l", 70) + strings.Repeat("e", 70))
	if _, err := Parse(data); err == nil {
		t.Error("expected depth-limit rejection, got nil")
	}
}

// encodeInfoTorrent wraps an info dict into a minimal bencoded .torrent.
func encodeInfoTorrent(t *testing.T, info map[string]interface{}) []byte {
	t.Helper()
	data, err := bencode.EncodeBytes(map[string]interface{}{
		"announce": "http://localhost:8080/announce",
		"info":     info,
	})
	if err != nil {
		t.Fatal(err)
	}
	return data
}

func TestParseRejectsPathTraversal(t *testing.T) {
	pieces := strings.Repeat("\x00", 20)
	cases := []struct {
		name string
		info map[string]interface{}
	}{
		{"v1 files dotdot", map[string]interface{}{
			"name": "ds", "piece length": int64(MinPieceLength), "pieces": pieces,
			"files": []interface{}{
				map[string]interface{}{"length": int64(4), "path": []interface{}{"..", "x"}},
			},
		}},
		{"v1 files dot", map[string]interface{}{
			"name": "ds", "piece length": int64(MinPieceLength), "pieces": pieces,
			"files": []interface{}{
				map[string]interface{}{"length": int64(4), "path": []interface{}{".", "x"}},
			},
		}},
		{"v1 files empty component", map[string]interface{}{
			"name": "ds", "piece length": int64(MinPieceLength), "pieces": pieces,
			"files": []interface{}{
				map[string]interface{}{"length": int64(4), "path": []interface{}{"", "x"}},
			},
		}},
		{"v1 files absolute component", map[string]interface{}{
			"name": "ds", "piece length": int64(MinPieceLength), "pieces": pieces,
			"files": []interface{}{
				map[string]interface{}{"length": int64(4), "path": []interface{}{"/etc", "passwd"}},
			},
		}},
		{"v1 files separator in component", map[string]interface{}{
			"name": "ds", "piece length": int64(MinPieceLength), "pieces": pieces,
			"files": []interface{}{
				map[string]interface{}{"length": int64(4), "path": []interface{}{"a/../../x"}},
			},
		}},
		{"v1 files backslash in component", map[string]interface{}{
			"name": "ds", "piece length": int64(MinPieceLength), "pieces": pieces,
			"files": []interface{}{
				map[string]interface{}{"length": int64(4), "path": []interface{}{"..\\x"}},
			},
		}},
		{"v1 files NUL in component", map[string]interface{}{
			"name": "ds", "piece length": int64(MinPieceLength), "pieces": pieces,
			"files": []interface{}{
				map[string]interface{}{"length": int64(4), "path": []interface{}{"x\x00y"}},
			},
		}},
		{"v2 file tree dotdot key", map[string]interface{}{
			"name": "ds", "piece length": int64(MinPieceLength), "meta version": int64(2),
			"file tree": map[string]interface{}{
				"..": map[string]interface{}{
					"x": map[string]interface{}{
						"": map[string]interface{}{"length": int64(4)},
					},
				},
			},
		}},
		{"v2 file tree dotdot leaf", map[string]interface{}{
			"name": "ds", "piece length": int64(MinPieceLength), "meta version": int64(2),
			"file tree": map[string]interface{}{
				"..": map[string]interface{}{
					"": map[string]interface{}{"length": int64(4)},
				},
			},
		}},
		{"v2 file tree absolute key", map[string]interface{}{
			"name": "ds", "piece length": int64(MinPieceLength), "meta version": int64(2),
			"file tree": map[string]interface{}{
				"/tmp": map[string]interface{}{
					"x": map[string]interface{}{
						"": map[string]interface{}{"length": int64(4)},
					},
				},
			},
		}},
		{"single file dotdot name", map[string]interface{}{
			"name": "..", "piece length": int64(MinPieceLength), "pieces": pieces,
			"length": int64(4),
		}},
		{"single file absolute name", map[string]interface{}{
			"name": "/etc/passwd", "piece length": int64(MinPieceLength), "pieces": pieces,
			"length": int64(4),
		}},
		{"single file empty name", map[string]interface{}{
			"name": "", "piece length": int64(MinPieceLength), "pieces": pieces,
			"length": int64(4),
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			data := encodeInfoTorrent(t, c.info)
			if _, err := Parse(data); err == nil {
				t.Errorf("expected rejection, got nil")
			}
			// ParseInfo shares the same code path; confirm it rejects too.
			infoBytes, err := bencode.EncodeBytes(c.info)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParseInfo(infoBytes); err == nil {
				t.Errorf("ParseInfo: expected rejection, got nil")
			}
		})
	}
}

func TestParseAcceptsNestedPaths(t *testing.T) {
	// 8 bytes total, 1 piece.
	pieces := strings.Repeat("\x00", 20)
	v1 := map[string]interface{}{
		"name": "ds", "piece length": int64(MinPieceLength), "pieces": pieces,
		"files": []interface{}{
			map[string]interface{}{"length": int64(4), "path": []interface{}{"sub", "deep", "a.txt"}},
			map[string]interface{}{"length": int64(4), "path": []interface{}{"b..txt"}},
		},
	}
	meta, err := Parse(encodeInfoTorrent(t, v1))
	if err != nil {
		t.Fatalf("v1 nested: %v", err)
	}
	if len(meta.Files) != 2 {
		t.Fatalf("v1 nested: want 2 files, got %d", len(meta.Files))
	}
	if want := filepath.Join("sub", "deep", "a.txt"); meta.Files[0].Path != want {
		t.Errorf("v1 nested: path = %q, want %q", meta.Files[0].Path, want)
	}
	if meta.Files[1].Path != "b..txt" {
		t.Errorf("v1 nested: path = %q, want b..txt", meta.Files[1].Path)
	}
	if meta.PieceCount != 1 {
		t.Errorf("v1 nested: piece count = %d, want 1", meta.PieceCount)
	}

	v2 := map[string]interface{}{
		"name": "ds", "piece length": int64(MinPieceLength), "meta version": int64(2),
		"file tree": map[string]interface{}{
			"sub": map[string]interface{}{
				"deep": map[string]interface{}{
					"a.txt": map[string]interface{}{
						"": map[string]interface{}{"length": int64(4)},
					},
				},
			},
		},
	}
	meta, err = Parse(encodeInfoTorrent(t, v2))
	if err != nil {
		t.Fatalf("v2 nested: %v", err)
	}
	if len(meta.Files) != 1 {
		t.Fatalf("v2 nested: want 1 file, got %d", len(meta.Files))
	}
	if want := filepath.Join("sub", "deep", "a.txt"); meta.Files[0].Path != want {
		t.Errorf("v2 nested: path = %q, want %q", meta.Files[0].Path, want)
	}
	if meta.Pieces != nil {
		t.Errorf("v2-only: Pieces should be nil, got %d bytes", len(meta.Pieces))
	}
	if meta.PieceCount != 1 {
		t.Errorf("v2-only: piece count = %d, want 1 (computed from size)", meta.PieceCount)
	}
}

func TestSafeJoin(t *testing.T) {
	bad := [][]string{
		nil, {""}, {"."}, {".."}, {"a", ".."}, {"..", "a"},
		{"a/b"}, {"a\\b"}, {"a\x00b"}, {"/a"}, {"/"},
	}
	for _, parts := range bad {
		if _, err := safeJoin(parts); err == nil {
			t.Errorf("safeJoin(%q): expected error", parts)
		}
	}
	good := map[string][]string{
		"a":                         {"a"},
		filepath.Join("a", "b"):     {"a", "b"},
		"..a":                       {"..a"},
		"a..":                       {"a.."},
		filepath.Join("a", "...b"):  {"a", "...b"},
		filepath.Join("x y", "z&w"): {"x y", "z&w"},
	}
	for want, parts := range good {
		got, err := safeJoin(parts)
		if err != nil {
			t.Errorf("safeJoin(%q): unexpected error %v", parts, err)
			continue
		}
		if got != want {
			t.Errorf("safeJoin(%q) = %q, want %q", parts, got, want)
		}
	}
}

func TestParsePiecesConsistency(t *testing.T) {
	base := func(pieces string, length int64) map[string]interface{} {
		return map[string]interface{}{
			"name": "f", "piece length": int64(MinPieceLength), "pieces": pieces,
			"length": length,
		}
	}
	t.Run("not multiple of 20", func(t *testing.T) {
		_, err := Parse(encodeInfoTorrent(t, base(strings.Repeat("\x00", 21), 4)))
		if err == nil || !strings.Contains(err.Error(), "multiple of 20") {
			t.Errorf("expected 'multiple of 20' error, got %v", err)
		}
	})
	t.Run("count mismatch too few", func(t *testing.T) {
		// 2 pieces worth of data but only 1 hash.
		_, err := Parse(encodeInfoTorrent(t, base(strings.Repeat("\x00", 20), int64(MinPieceLength)+1)))
		if err == nil {
			t.Error("expected piece count mismatch error, got nil")
		}
	})
	t.Run("count mismatch too many", func(t *testing.T) {
		// 1 piece worth of data but 2 hashes.
		_, err := Parse(encodeInfoTorrent(t, base(strings.Repeat("\x00", 40), 4)))
		if err == nil {
			t.Error("expected piece count mismatch error, got nil")
		}
	})
	t.Run("count matches", func(t *testing.T) {
		meta, err := Parse(encodeInfoTorrent(t, base(strings.Repeat("\x00", 40), int64(MinPieceLength)+1)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta.PieceCount != 2 {
			t.Errorf("piece count = %d, want 2", meta.PieceCount)
		}
		if len(meta.Pieces) != 40 {
			t.Errorf("pieces len = %d, want 40", len(meta.Pieces))
		}
	})
	t.Run("multi-file count matches", func(t *testing.T) {
		info := map[string]interface{}{
			"name": "ds", "piece length": int64(MinPieceLength), "pieces": strings.Repeat("\x00", 40),
			"files": []interface{}{
				map[string]interface{}{"length": int64(MinPieceLength), "path": []interface{}{"a"}},
				map[string]interface{}{"length": int64(1), "path": []interface{}{"b"}},
			},
		}
		if _, err := Parse(encodeInfoTorrent(t, info)); err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	})
	t.Run("empty file zero pieces", func(t *testing.T) {
		meta, err := Parse(encodeInfoTorrent(t, base("", 0)))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta.PieceCount != 0 {
			t.Errorf("piece count = %d, want 0", meta.PieceCount)
		}
	})
	t.Run("pieces absent leaves Pieces nil", func(t *testing.T) {
		info := map[string]interface{}{
			"name": "f", "piece length": int64(MinPieceLength), "length": int64(MinPieceLength) + 1,
		}
		meta, err := Parse(encodeInfoTorrent(t, info))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if meta.Pieces != nil {
			t.Error("Pieces should be nil when absent")
		}
		if meta.PieceCount != 2 {
			t.Errorf("piece count = %d, want 2", meta.PieceCount)
		}
	})
}

func TestParseRejectsNonPositivePieceLength(t *testing.T) {
	for _, length := range []int64{-1, 0} {
		t.Run(fmt.Sprintf("length %d", length), func(t *testing.T) {
			info := map[string]interface{}{
				"name": "f", "piece length": length, "length": int64(4),
			}
			_, err := Parse(encodeInfoTorrent(t, info))
			if err == nil || !strings.Contains(err.Error(), "must be positive") {
				t.Fatalf("expected 'must be positive' error, got %v", err)
			}
		})
	}
}

func TestParseRejectsMissingOrWrongTypePieceLength(t *testing.T) {
	pieces := strings.Repeat("\x00", 20)
	cases := map[string]map[string]interface{}{
		"missing": {
			"name": "f", "length": int64(1), "pieces": pieces,
		},
		"string instead of int": {
			"name": "f", "piece length": "16384", "length": int64(1), "pieces": pieces,
		},
	}
	for name, info := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Parse(encodeInfoTorrent(t, info))
			if err == nil || !strings.Contains(err.Error(), "piece length") {
				t.Fatalf("expected piece length error, got %v", err)
			}
		})
	}
}

func TestVerifyInfoHash(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "verify_hash")
	if err := os.WriteFile(path, []byte("verify hash content"), 0644); err != nil {
		t.Fatal(err)
	}
	result, err := Create(CreateOptions{
		Path:        path,
		Name:        "verify_hash",
		PieceLength: MinPieceLength,
		AnnounceURL: "http://localhost:8080/announce",
	})
	if err != nil {
		t.Fatalf("create failed: %v", err)
	}

	infoBytes, err := ExtractInfoBytes(result.TorrentBytes)
	if err != nil {
		t.Fatalf("ExtractInfoBytes failed: %v", err)
	}

	t.Run("matching hybrid magnet verifies", func(t *testing.T) {
		mag := Magnet{InfoHashV1: result.InfoHashV1Hex, InfoHashV2: result.InfoHashHex}
		if err := VerifyInfoHash(infoBytes, mag); err != nil {
			t.Errorf("expected verification to pass, got %v", err)
		}
	})

	t.Run("v1-only magnet ignores v2", func(t *testing.T) {
		mag := Magnet{InfoHashV1: result.InfoHashV1Hex}
		if err := VerifyInfoHash(infoBytes, mag); err != nil {
			t.Errorf("expected verification to pass, got %v", err)
		}
	})

	t.Run("wrong v1 hash rejected", func(t *testing.T) {
		mag := Magnet{InfoHashV1: strings.Repeat("a", 40)}
		if err := VerifyInfoHash(infoBytes, mag); err == nil {
			t.Error("expected verification to fail on v1 mismatch")
		}
	})

	t.Run("wrong v2 hash rejected even when v1 matches", func(t *testing.T) {
		mag := Magnet{InfoHashV1: result.InfoHashV1Hex, InfoHashV2: strings.Repeat("b", 64)}
		if err := VerifyInfoHash(infoBytes, mag); err == nil {
			t.Error("expected verification to fail on v2 mismatch")
		}
	})

	t.Run("substituted metadata rejected", func(t *testing.T) {
		other := filepath.Join(dir, "other")
		if err := os.WriteFile(other, []byte("different content entirely"), 0644); err != nil {
			t.Fatal(err)
		}
		otherResult, err := Create(CreateOptions{
			Path:        other,
			Name:        "other",
			PieceLength: MinPieceLength,
			AnnounceURL: "http://localhost:8080/announce",
		})
		if err != nil {
			t.Fatal(err)
		}
		otherInfoBytes, err := ExtractInfoBytes(otherResult.TorrentBytes)
		if err != nil {
			t.Fatal(err)
		}

		// The magnet names the first torrent's hash but infoBytes here is the
		// second torrent's metadata (path/size/piece-hash substitution).
		mag := Magnet{InfoHashV1: result.InfoHashV1Hex, InfoHashV2: result.InfoHashHex}
		if err := VerifyInfoHash(otherInfoBytes, mag); err == nil {
			t.Error("expected substituted metadata to fail verification")
		}
	})
}
