package torrent

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/zeebo/bencode"

	wbencode "weightless/internal/bencode"
)

// maxFileTreeDepth bounds BEP 52 file-tree recursion. The bencode validator
// already caps overall nesting depth, but we keep an explicit guard here as
// defense-in-depth — walkFileTree must not recurse on attacker-controlled
// structure even if a future caller skips Validate.
const maxFileTreeDepth = 64

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
// LangSec: structurally validate against TorrentLimits before letting the
// permissive decoder allocate anything.
func Parse(data []byte) (TorrentMeta, error) {
	if err := wbencode.Validate(data, wbencode.TorrentLimits); err != nil {
		return TorrentMeta{}, fmt.Errorf("torrent validate: %w", err)
	}
	var raw map[string]interface{}
	if err := bencode.DecodeBytes(data, &raw); err != nil {
		return TorrentMeta{}, fmt.Errorf("bencode decode: %w", err)
	}

	info, ok := raw["info"].(map[string]interface{})
	if !ok {
		return TorrentMeta{}, fmt.Errorf("missing or invalid info dict")
	}

	return parseInfoMap(info)
}

// ParseInfo decodes a bare bencoded info dictionary (the value of the "info"
// key, as returned by BEP 9 ut_metadata exchange) into TorrentMeta. Same LangSec
// posture as Parse: validate the structure before the permissive decoder runs.
func ParseInfo(infoData []byte) (TorrentMeta, error) {
	if err := wbencode.Validate(infoData, wbencode.TorrentLimits); err != nil {
		return TorrentMeta{}, fmt.Errorf("info dict validate: %w", err)
	}
	var info map[string]interface{}
	if err := bencode.DecodeBytes(infoData, &info); err != nil {
		return TorrentMeta{}, fmt.Errorf("bencode decode: %w", err)
	}
	return parseInfoMap(info)
}

// parseInfoMap extracts TorrentMeta fields from a decoded info dict. Shared by
// Parse (full .torrent) and ParseInfo (bare info dict from a peer).
//
// Every file path is validated with safeJoin before it lands in TorrentMeta:
// the info dict is attacker-controlled (registry download or BEP 9 exchange)
// and the client joins these paths onto its output directory.
func parseInfoMap(info map[string]interface{}) (TorrentMeta, error) {
	meta := TorrentMeta{}
	if v, ok := info["name"].(string); ok {
		meta.Name = v
	}
	if v, ok := info["piece length"].(int64); ok {
		meta.PieceLength = int(v)
	}
	if v, ok := info["pieces"].(string); ok {
		if len(v)%20 != 0 {
			return TorrentMeta{}, fmt.Errorf("pieces length not a multiple of 20")
		}
		meta.Pieces = []byte(v)
		meta.PieceCount = len(meta.Pieces) / 20
	}

	// Single file
	if length, ok := info["length"].(int64); ok {
		path, err := safeJoin([]string{meta.Name})
		if err != nil {
			return TorrentMeta{}, fmt.Errorf("name: %w", err)
		}
		meta.TotalSize = length
		meta.Files = []FileEntry{{Path: path, Length: length}}
	}

	// Multi-file (v1)
	if files, ok := info["files"].([]interface{}); ok {
		for i, f := range files {
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
				path, err := safeJoin(parts)
				if err != nil {
					return TorrentMeta{}, fmt.Errorf("files[%d] path: %w", i, err)
				}
				fe.Path = path
			}
			meta.Files = append(meta.Files, fe)
		}
	}

	// If no v1 files list, try v2 file tree (BEP 52)
	if len(meta.Files) == 0 {
		if fileTree, ok := info["file tree"].(map[string]interface{}); ok {
			files, err := walkFileTree(fileTree, nil, 0)
			if err != nil {
				return TorrentMeta{}, fmt.Errorf("file tree: %w", err)
			}
			meta.Files = files
			for _, f := range meta.Files {
				meta.TotalSize += f.Length
			}
		}
	}

	// Cross-check the v1 pieces string against the declared layout. A
	// mismatch means the piece-to-file offset math would be wrong downstream.
	if meta.Pieces != nil && meta.PieceLength > 0 && len(meta.Files) > 0 {
		want := int((meta.TotalSize + int64(meta.PieceLength) - 1) / int64(meta.PieceLength))
		if meta.PieceCount != want {
			return TorrentMeta{}, fmt.Errorf("pieces count %d does not match total size %d / piece length %d (want %d)",
				meta.PieceCount, meta.TotalSize, meta.PieceLength, want)
		}
	}

	// Re-calculate piece count if not set by v1 pieces string
	if meta.PieceCount == 0 && meta.PieceLength > 0 && meta.TotalSize > 0 {
		meta.PieceCount = int((meta.TotalSize + int64(meta.PieceLength) - 1) / int64(meta.PieceLength))
	}

	return meta, nil
}

// safeJoin joins torrent-supplied path components into a relative path,
// rejecting anything that could escape the download directory: empty
// components, "." and "..", components containing a path separator (either
// flavour) or NUL, and absolute paths.
func safeJoin(parts []string) (string, error) {
	if len(parts) == 0 {
		return "", fmt.Errorf("empty path")
	}
	for _, p := range parts {
		switch {
		case p == "":
			return "", fmt.Errorf("empty path component")
		case p == "." || p == "..":
			return "", fmt.Errorf("path component %q not allowed", p)
		case strings.ContainsAny(p, "/\\\x00"):
			return "", fmt.Errorf("path component %q contains separator or NUL", p)
		case filepath.IsAbs(p) || filepath.VolumeName(p) != "":
			// Separators are already rejected above; this catches Windows
			// drive-relative forms like "C:foo" that Join would not neutralise.
			return "", fmt.Errorf("absolute path component %q not allowed", p)
		}
	}
	joined := filepath.Join(parts...)
	if filepath.IsAbs(joined) {
		return "", fmt.Errorf("absolute path %q not allowed", joined)
	}
	return joined, nil
}

// walkFileTree recursively walks a BEP 52 file tree and collects file entries.
// depth is the current recursion level — bailing out at maxFileTreeDepth
// caps stack use even if upstream validation was bypassed. Map keys are the
// path components and are validated via safeJoin on every leaf.
func walkFileTree(tree map[string]interface{}, prefix []string, depth int) ([]FileEntry, error) {
	if depth > maxFileTreeDepth {
		return nil, nil
	}
	var files []FileEntry
	for name, val := range tree {
		node, ok := val.(map[string]interface{})
		if !ok {
			continue
		}
		parts := append(append([]string(nil), prefix...), name)
		// Leaf node: has "" key with length
		if leaf, ok := node[""].(map[string]interface{}); ok {
			path, err := safeJoin(parts)
			if err != nil {
				return nil, err
			}
			var length int64
			if l, ok := leaf["length"].(int64); ok {
				length = l
			}
			files = append(files, FileEntry{Path: path, Length: length})
		} else {
			// Directory node: validate the component before recursing so a
			// bad directory name is rejected even if it has no leaves.
			if _, err := safeJoin(parts); err != nil {
				return nil, err
			}
			sub, err := walkFileTree(node, parts, depth+1)
			if err != nil {
				return nil, err
			}
			files = append(files, sub...)
		}
	}
	return files, nil
}
