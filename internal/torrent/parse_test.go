package torrent

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
