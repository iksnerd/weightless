package client

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// newTestStorage creates a Storage for tests and closes its root on cleanup.
func newTestStorage(t *testing.T, dir string, files []FileEntry) *Storage {
	t.Helper()
	s, err := NewStorage(dir, files)
	if err != nil {
		t.Fatalf("NewStorage: %v", err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

func TestPreallocateSingleFile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newTestStorage(t, dir, []FileEntry{{Path: "test.dat", Length: 1024}})

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
	s := newTestStorage(t, dir, []FileEntry{
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
	s := newTestStorage(t, dir, []FileEntry{{Path: "out.dat", Length: 32}})
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
	s := newTestStorage(t, dir, []FileEntry{
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

func TestPreallocateRejectsPathTraversal(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	dir := filepath.Join(parent, "download")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}

	cases := []string{
		"../escape",
		"../../escape",
		"sub/../../escape",
		"..",
		".",
		"",
		filepath.Join(parent, "abs-escape"),
	}
	for _, p := range cases {
		s := newTestStorage(t, dir, []FileEntry{{Path: p, Length: 16}})
		if err := s.Preallocate(); err == nil {
			t.Errorf("Preallocate(%q): expected error, got nil", p)
		}
		if err := s.WritePiece(0, 16, make([]byte, 16)); err == nil {
			t.Errorf("WritePiece(%q): expected error, got nil", p)
		}
	}

	// Nothing may have been created outside the download dir.
	entries, err := os.ReadDir(parent)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if e.Name() != "download" {
			t.Errorf("unexpected entry outside download dir: %s", e.Name())
		}
	}
	if _, err := os.Stat(filepath.Join(parent, "escape")); !os.IsNotExist(err) {
		t.Errorf("../escape was created (stat err = %v)", err)
	}
}

// TestPreallocateRejectsSymlinkEscape covers the gap a purely textual
// containment check misses: a directory component under baseDir that is
// actually a symlink pointing outside it (e.g. left behind by another
// torrent sharing the same download directory). The file path itself
// contains no "..", so string-prefix matching on the joined path would
// wrongly call it contained; only refusing to follow the symlink (via
// os.Root) catches it.
func TestPreallocateRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires elevated privileges on windows")
	}
	t.Parallel()

	outside := t.TempDir()
	dir := t.TempDir()

	link := filepath.Join(dir, "shared")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}

	s := newTestStorage(t, dir, []FileEntry{{Path: "shared/evil.dat", Length: 16}})
	if err := s.Preallocate(); err == nil {
		t.Error("Preallocate through a symlinked directory: expected error, got nil")
	}
	if err := s.WritePiece(0, 16, make([]byte, 16)); err == nil {
		t.Error("WritePiece through a symlinked directory: expected error, got nil")
	}

	if _, err := os.Stat(filepath.Join(outside, "evil.dat")); !os.IsNotExist(err) {
		t.Errorf("evil.dat was created outside baseDir via symlink (stat err = %v)", err)
	}
}

func TestResolveContainment(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	s := newTestStorage(t, dir, nil)

	// A sibling directory sharing baseDir as a string prefix must not pass.
	sibling := "../" + filepath.Base(dir) + "-sibling/x"
	if _, err := s.resolve(sibling); err == nil {
		t.Errorf("resolve(%q): expected error (prefix-sibling escape)", sibling)
	}

	got, err := s.resolve("a/b.txt")
	if err != nil {
		t.Fatalf("resolve(a/b.txt): %v", err)
	}
	want := filepath.Join("a", "b.txt")
	if got != want {
		t.Errorf("resolve = %q, want %q", got, want)
	}
	// Inner ".." that stays inside baseDir is tolerated by the containment
	// check (the parser rejects it upstream anyway).
	if _, err := s.resolve("a/../b.txt"); err != nil {
		t.Errorf("resolve(a/../b.txt): unexpected error %v", err)
	}
}

func TestNewStorageAbsBaseDir(t *testing.T) {
	t.Chdir(t.TempDir())
	s := newTestStorage(t, "relative/dir", nil)
	if !filepath.IsAbs(s.baseDir) {
		t.Errorf("baseDir should be absolute, got %q", s.baseDir)
	}
}
