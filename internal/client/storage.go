package client

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Storage handles writing piece data to the correct files on disk.
type Storage struct {
	baseDir string
	root    *os.Root
	files   []FileEntry
}

// NewStorage creates a new storage manager rooted at baseDir. baseDir is
// created if missing and opened as an os.Root: every subsequent file
// operation goes through that root, so the OS refuses to resolve a path
// component through a symlink that would land outside baseDir, even if one
// is planted there after this call (e.g. by another torrent sharing the same
// download directory). A textual containment check on its own can't catch
// that — the check and the open are two different syscalls, and the target
// of a symlink can change, or come into existence, between them.
func NewStorage(baseDir string, files []FileEntry) (*Storage, error) {
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		abs = filepath.Clean(baseDir)
	}
	if err := os.MkdirAll(abs, 0755); err != nil {
		return nil, fmt.Errorf("create download dir %s: %w", abs, err)
	}
	root, err := os.OpenRoot(abs)
	if err != nil {
		return nil, fmt.Errorf("open download dir %s: %w", abs, err)
	}
	return &Storage{
		baseDir: abs,
		root:    root,
		files:   files,
	}, nil
}

// Close releases the root directory handle.
func (s *Storage) Close() error {
	return s.root.Close()
}

// resolve validates a torrent-supplied relative file path and returns it
// cleaned, still relative to baseDir. The torrent parser already rejects
// ".." and absolute components; this is defense in depth for any FileEntry
// that reaches Storage by another route. It does NOT resolve symlinks —
// escape-via-symlink is instead prevented at the syscall level by s.root.
func (s *Storage) resolve(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty file path")
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", fmt.Errorf("absolute file path %q not allowed", rel)
	}
	cleaned := filepath.Clean(rel)
	if cleaned == "." {
		return "", fmt.Errorf("file path %q resolves to the download directory itself", rel)
	}
	if cleaned == ".." || strings.HasPrefix(cleaned, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("file path %q escapes download directory", rel)
	}
	return cleaned, nil
}

// Preallocate creates the necessary directories and files on disk.
func (s *Storage) Preallocate() error {
	for _, fe := range s.files {
		rel, err := s.resolve(fe.Path)
		if err != nil {
			return err
		}
		if dir := filepath.Dir(rel); dir != "." {
			if err := s.root.MkdirAll(dir, 0755); err != nil {
				return fmt.Errorf("mkdir %s: %w", dir, err)
			}
		}

		// Create or open the file
		f, err := s.root.OpenFile(rel, os.O_RDWR|os.O_CREATE, 0644)
		if err != nil {
			return fmt.Errorf("open %s: %w", rel, err)
		}

		// Preallocate size (simple truncate)
		if err := f.Truncate(fe.Length); err != nil {
			f.Close()
			return fmt.Errorf("truncate %s: %w", rel, err)
		}
		f.Close()
	}
	return nil
}

// WritePiece writes a full verified piece to the correct file(s).
func (s *Storage) WritePiece(pieceIndex int, pieceLength int, data []byte) error {
	globalOffset := int64(pieceIndex) * int64(pieceLength)
	dataOffset := 0
	bytesToWrite := len(data)

	var currentPos int64 = 0
	for _, fe := range s.files {
		fileEnd := currentPos + fe.Length

		// Check if this piece starts in this file or overlaps with it
		if globalOffset < fileEnd && globalOffset+int64(bytesToWrite) > currentPos {
			// This piece has data for this file

			// Calculate where in the file to start writing
			writeAt := int64(0)
			if globalOffset > currentPos {
				writeAt = globalOffset - currentPos
			}

			// Calculate how many bytes from the piece go into this file
			startInPiece := int64(0)
			if currentPos > globalOffset {
				startInPiece = currentPos - globalOffset
			}

			endInPiece := startInPiece + (fe.Length - writeAt)
			if endInPiece > int64(bytesToWrite) {
				endInPiece = int64(bytesToWrite)
			}

			toWrite := data[startInPiece:endInPiece]

			rel, err := s.resolve(fe.Path)
			if err != nil {
				return err
			}
			f, err := s.root.OpenFile(rel, os.O_RDWR, 0644)
			if err != nil {
				return err
			}

			_, err = f.WriteAt(toWrite, writeAt)
			f.Close()
			if err != nil {
				return err
			}

			dataOffset += len(toWrite)
		}
		currentPos = fileEnd
	}

	// Every byte of the piece must have mapped to a file; otherwise the
	// piece-to-file offset math is wrong and we'd silently drop data.
	if dataOffset != bytesToWrite {
		return fmt.Errorf("piece %d: wrote %d of %d bytes (offset mapping mismatch)", pieceIndex, dataOffset, bytesToWrite)
	}

	return nil
}
