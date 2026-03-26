package client

import (
	"fmt"
	"os"
	"path/filepath"
)

// Storage handles writing piece data to the correct files on disk.
type Storage struct {
	baseDir string
	files   []FileEntry
}

// NewStorage creates a new storage manager.
func NewStorage(baseDir string, files []FileEntry) *Storage {
	return &Storage{
		baseDir: baseDir,
		files:   files,
	}
}

// Preallocate creates the necessary directories and files on disk.
func (s *Storage) Preallocate() error {
	for _, fe := range s.files {
		path := filepath.Join(s.baseDir, fe.Path)
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

			path := filepath.Join(s.baseDir, fe.Path)
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

	return nil
}
