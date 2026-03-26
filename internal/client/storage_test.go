package client

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPreallocateSingleFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewStorage(dir, []FileEntry{{Path: "test.dat", Length: 1024}})

	if err := s.Preallocate(); err != nil {
		t.Fatal(err)
	}

	info, err := os.Stat(filepath.Join(dir, "test.dat"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != 1024 {
		t.Errorf("expected size 1024, got %d", info.Size())
	}
}

func TestPreallocateNestedDirs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewStorage(dir, []FileEntry{
		{Path: "sub/deep/a.txt", Length: 100},
		{Path: "sub/b.txt", Length: 200},
	})

	if err := s.Preallocate(); err != nil {
		t.Fatal(err)
	}

	info, _ := os.Stat(filepath.Join(dir, "sub", "deep", "a.txt"))
	if info.Size() != 100 {
		t.Errorf("a.txt size = %d, want 100", info.Size())
	}
	info, _ = os.Stat(filepath.Join(dir, "sub", "b.txt"))
	if info.Size() != 200 {
		t.Errorf("b.txt size = %d, want 200", info.Size())
	}
}

func TestWritePieceSingleFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := NewStorage(dir, []FileEntry{{Path: "out.dat", Length: 32}})
	s.Preallocate()

	// Write piece 0 (first 16 bytes)
	data := []byte("0123456789abcdef")
	if err := s.WritePiece(0, 16, data); err != nil {
		t.Fatal(err)
	}

	// Write piece 1 (next 16 bytes)
	data2 := []byte("ABCDEFGHIJKLMNOP")
	if err := s.WritePiece(1, 16, data2); err != nil {
		t.Fatal(err)
	}

	got, _ := os.ReadFile(filepath.Join(dir, "out.dat"))
	expected := "0123456789abcdefABCDEFGHIJKLMNOP"
	if string(got) != expected {
		t.Errorf("file content = %q, want %q", string(got), expected)
	}
}

func TestWritePieceSpansFiles(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	// Two files: 10 bytes + 6 bytes = 16 bytes total, one piece
	s := NewStorage(dir, []FileEntry{
		{Path: "first.dat", Length: 10},
		{Path: "second.dat", Length: 6},
	})
	s.Preallocate()

	data := []byte("AAAAAAAAAABBBBBB") // 10 A's + 6 B's
	if err := s.WritePiece(0, 16, data); err != nil {
		t.Fatal(err)
	}

	got1, _ := os.ReadFile(filepath.Join(dir, "first.dat"))
	if string(got1) != "AAAAAAAAAA" {
		t.Errorf("first.dat = %q, want 10 A's", string(got1))
	}

	got2, _ := os.ReadFile(filepath.Join(dir, "second.dat"))
	if string(got2) != "BBBBBB" {
		t.Errorf("second.dat = %q, want 6 B's", string(got2))
	}
}

func TestBlockSize(t *testing.T) {
	t.Parallel()
	if blockSize(32768, 0) != BlockSize {
		t.Error("expected BlockSize for large remaining")
	}
	if blockSize(16384, 16000) != 384 {
		t.Errorf("expected 384 for last block, got %d", blockSize(16384, 16000))
	}
	if blockSize(100, 0) != 100 {
		t.Error("expected 100 for tiny piece")
	}
}
