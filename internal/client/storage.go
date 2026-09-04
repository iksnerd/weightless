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
	files   []FileEntry
}

// NewStorage creates a new storage manager. baseDir is normalised to a clean
// absolute path so that resolve can do a reliable containment check.
func NewStorage(baseDir string, files []FileEntry) *Storage {
	abs, err := filepath.Abs(baseDir)
	if err != nil {
		abs = filepath.Clean(baseDir)
	}
	return &Storage{
		baseDir: abs,
		files:   files,
	}
}

// resolve joins a torrent-supplied relative path onto baseDir and verifies
// the result stays inside baseDir. The torrent parser already rejects ".."
// and absolute components; this is defense in depth for any FileEntry that
// reaches Storage by another route.
func (s *Storage) resolve(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("empty file path")
	}
	if filepath.IsAbs(rel) || filepath.VolumeName(rel) != "" {
		return "", fmt.Errorf("absolute file path %q not allowed", rel)
	}
	joined := filepath.Clean(filepath.Join(s.baseDir, rel))
	if joined == s.baseDir {
		return "", fmt.Errorf("file path %q resolves to the download directory itself", rel)
	}
	if !strings.HasPrefix(joined, s.baseDir+string(filepath.Separator)) {
		return "", fmt.Errorf("file path %q escapes download directory", rel)
	}
	return joined, nil
}

// Preallocate creates the necessary directories and files on disk.
func (s *Storage) Preallocate() error {
	for _, fe := range s.files {
		path, err := s.resolve(fe.Path)
		if err != nil {
			return err
		}
		dir := filepath.Dir(path)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}

		// Create or open the file
		f, err := os.OpenFile(path, os.O_RDWR|os.O_CREATE, 0644)
		if err != nil {
			return fmt.Errorf("open %s: %w", path, err)
		}

		// Preallocate size (simple truncate)
		if err := f.Truncate(fe.Length); err != nil {
			f.Close()
			return fmt.Errorf("truncate %s: %w", path, err)
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

			path, err := s.resolve(fe.Path)
			if err != nil {
				return err
			}
			f, err := os.OpenFile(path, os.O_RDWR, 0644)
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
