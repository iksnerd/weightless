package torrent

import (
	"fmt"
	"path/filepath"

	"github.com/zeebo/bencode"
)

// TorrentMeta holds parsed torrent metadata for display or registry use.
type TorrentMeta struct {
	Name        string      `json:"name"`
	PieceLength int         `json:"piece_length"`
	PieceCount  int         `json:"piece_count"`
	TotalSize   int64       `json:"total_size"`
	Files       []FileEntry `json:"files"`
	Pieces      []byte      `json:"-"` // SHA-1 hashes (binary)
}

type FileEntry struct {
	Path   string `json:"path"`
	Length int64  `json:"length"`
}

// Parse decodes a bencoded .torrent file into display-friendly metadata.
func Parse(data []byte) (TorrentMeta, error) {
	var raw map[string]interface{}
	if err := bencode.DecodeBytes(data, &raw); err != nil {
		return TorrentMeta{}, fmt.Errorf("bencode decode: %w", err)
	}

	info, ok := raw["info"].(map[string]interface{})
	if !ok {
		return TorrentMeta{}, fmt.Errorf("missing or invalid info dict")
	}

	meta := TorrentMeta{}
	if v, ok := info["name"].(string); ok {
		meta.Name = v
	}
	if v, ok := info["piece length"].(int64); ok {
		meta.PieceLength = int(v)
	}
	if v, ok := info["pieces"].(string); ok {
		meta.Pieces = []byte(v)
		meta.PieceCount = len(meta.Pieces) / 20
	}

	// Single file
	if length, ok := info["length"].(int64); ok {
		meta.TotalSize = length
		meta.Files = []FileEntry{{Path: meta.Name, Length: length}}
	}

	// Multi-file (v1)
	if files, ok := info["files"].([]interface{}); ok {
		for _, f := range files {
			dict, ok := f.(map[string]interface{})
			if !ok {
				continue
			}
			fe := FileEntry{}
			if l, ok := dict["length"].(int64); ok {
				fe.Length = l
				meta.TotalSize += l
			}
			if pathParts, ok := dict["path"].([]interface{}); ok {
				var parts []string
				for _, p := range pathParts {
					if s, ok := p.(string); ok {
						parts = append(parts, s)
					}
				}
				fe.Path = filepath.Join(parts...)
			}
			meta.Files = append(meta.Files, fe)
		}
	}

	// If no v1 files list, try v2 file tree (BEP 52)
	if len(meta.Files) == 0 {
		if fileTree, ok := info["file tree"].(map[string]interface{}); ok {
			meta.Files = walkFileTree(fileTree, "")
			for _, f := range meta.Files {
				meta.TotalSize += f.Length
			}
		}
	}

	// Re-calculate piece count if not set by v1 pieces string
	if meta.PieceCount == 0 && meta.PieceLength > 0 && meta.TotalSize > 0 {
		meta.PieceCount = int((meta.TotalSize + int64(meta.PieceLength) - 1) / int64(meta.PieceLength))
	}

	return meta, nil
}

// walkFileTree recursively walks a BEP 52 file tree and collects file entries.
func walkFileTree(tree map[string]interface{}, prefix string) []FileEntry {
	var files []FileEntry
	for name, val := range tree {
		node, ok := val.(map[string]interface{})
		if !ok {
			continue
		}
		path := filepath.Join(prefix, name)
		// Leaf node: has "" key with length
		if leaf, ok := node[""].(map[string]interface{}); ok {
			var length int64
			if l, ok := leaf["length"].(int64); ok {
				length = l
			}
			files = append(files, FileEntry{Path: path, Length: length})
		} else {
			// Directory node: recurse
			files = append(files, walkFileTree(node, path)...)
		}
	}
	return files
}
