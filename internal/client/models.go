package client

// TorrentMeta holds the metadata needed for downloading.
// This matches the structure in cmd/wl/main.go but is exported for package boundary use.
type TorrentMeta struct {
	Name        string
	InfoHashV1  []byte
	PieceLength int
	PieceCount  int
	TotalSize   int64
	Pieces      []byte // Concatenated SHA-1 hashes
	Files       []FileEntry
}

type FileEntry struct {
	Path   string
	Length int64
}
