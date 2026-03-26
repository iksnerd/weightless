package torrent

import (
	"crypto/sha1"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"math/bits"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/zeebo/bencode"
)

const (
	BlockSize          = 16384      // 16 KiB — BEP 52 fixed block size for Merkle trees
	DefaultPieceLength = 256 * 1024 // 256 KiB
	MinPieceLength     = 16 * 1024  // 16 KiB per BEP 52
)

// CreateOptions configures torrent creation.
type CreateOptions struct {
	Path        string // file or directory to torrent
	Name        string // torrent name (defaults to basename of Path)
	PieceLength int    // must be power of 2, >= MinPieceLength
	AnnounceURL string // tracker announce URL
	Private     bool   // if true, disables DHT/PEX (BEP 27)
	Comment     string // optional comment
	Source      string // source tag in info dict (e.g. "weightless.ai")
	CreatedBy   string // created by field (e.g. "Weightless CLI v1.0")
}

// CreateResult holds the output of torrent creation.
type CreateResult struct {
	TorrentBytes  []byte   // bencoded .torrent file content
	InfoHash      [32]byte // SHA-256 of bencoded info dict (v2)
	InfoHashHex   string   // hex-encoded v2 info hash
	InfoHashV1    [20]byte // SHA-1 of bencoded info dict (v1)
	InfoHashV1Hex string   // hex-encoded v1 info hash
	MagnetLink    string   // full magnet URI
}

// fileResult holds the v1 + v2 hashing results for a single file.
type fileResult struct {
	size           int64
	v2Entry        map[string]interface{} // file tree entry (length, pieces root)
	pieceLayerHash [][32]byte             // piece layer for piece layers dict
	v1PiecesSHA1   []byte                 // concatenated SHA-1 hashes of each piece (v1)
}

// Create builds a hybrid BitTorrent v1+v2 (BEP 52) .torrent file.
// Hybrid format is required for compatibility with Transmission 4.x and most clients.
func Create(opts CreateOptions) (*CreateResult, error) {
	if opts.PieceLength == 0 {
		opts.PieceLength = DefaultPieceLength
	}
	if opts.AnnounceURL == "" {
		return nil, fmt.Errorf("announce URL is required")
	}
	if opts.PieceLength < MinPieceLength || !isPowerOfTwo(opts.PieceLength) {
		return nil, fmt.Errorf("piece length must be a power of 2 and >= %d, got %d", MinPieceLength, opts.PieceLength)
	}

	info, err := os.Stat(opts.Path)
	if err != nil {
		return nil, fmt.Errorf("cannot access path: %w", err)
	}

	if opts.Name == "" {
		opts.Name = filepath.Base(opts.Path)
	}

	// Build the info dict with both v1 and v2 fields
	infoDict := newOrderedDict()
	pieceLayers := make(map[string]string)

	if info.IsDir() {
		err = buildDirHybrid(infoDict, pieceLayers, opts)
	} else {
		err = buildSingleFileHybrid(infoDict, pieceLayers, opts)
	}
	if err != nil {
		return nil, err
	}

	infoBytes, err := bencode.EncodeBytes(infoDict)
	if err != nil {
		return nil, fmt.Errorf("bencode info dict: %w", err)
	}

	infoHash := sha256.Sum256(infoBytes)
	infoHashV1 := sha1.Sum(infoBytes)

	metaDict := newOrderedDict()
	metaDict.set("announce", opts.AnnounceURL)
	if opts.CreatedBy != "" {
		metaDict.set("created by", opts.CreatedBy)
	}
	metaDict.set("creation date", time.Now().Unix())
	if opts.Comment != "" {
		metaDict.set("comment", opts.Comment)
	}
	metaDict.set("info", infoDict)
	if len(pieceLayers) > 0 {
		metaDict.set("piece layers", pieceLayers)
	}

	torrentBytes, err := bencode.EncodeBytes(metaDict)
	if err != nil {
		return nil, fmt.Errorf("bencode metainfo: %w", err)
	}

	hashHex := hex.EncodeToString(infoHash[:])
	v1Hex := hex.EncodeToString(infoHashV1[:])
	// Hybrid magnet: include both v1 (btih) and v2 (btmh) hashes
	magnet := fmt.Sprintf("magnet:?xt=urn:btih:%s&xt=urn:btmh:1220%s&dn=%s&tr=%s",
		v1Hex, hashHex, opts.Name, opts.AnnounceURL)

	return &CreateResult{
		TorrentBytes:  torrentBytes,
		InfoHash:      infoHash,
		InfoHashHex:   hashHex,
		InfoHashV1:    infoHashV1,
		InfoHashV1Hex: hex.EncodeToString(infoHashV1[:]),
		MagnetLink:    magnet,
	}, nil
}

// buildSingleFileHybrid populates the info dict for a single-file hybrid torrent.
func buildSingleFileHybrid(infoDict *orderedDict, pieceLayers map[string]string, opts CreateOptions) error {
	fr, err := hashFileHybrid(opts.Path, opts.PieceLength)
	if err != nil {
		return err
	}

	fileTree := map[string]interface{}{
		opts.Name: map[string]interface{}{
			"": fr.v2Entry,
		},
	}

	// v2 fields
	infoDict.set("file tree", fileTree)
	// v1 field: length (single file)
	infoDict.set("length", int(fr.size))
	// v2 field
	infoDict.set("meta version", 2)
	// shared
	infoDict.set("name", opts.Name)
	infoDict.set("piece length", opts.PieceLength)
	// v1 field: pieces (SHA-1 hashes)
	infoDict.set("pieces", string(fr.v1PiecesSHA1))
	// branding
	if opts.Source != "" {
		infoDict.set("source", opts.Source)
	}

	if opts.Private {
		infoDict.set("private", 1)
	}

	if len(fr.pieceLayerHash) > 0 {
		rootStr := fr.v2Entry["pieces root"].(string)
		var concat []byte
		for _, h := range fr.pieceLayerHash {
			concat = append(concat, h[:]...)
		}
		pieceLayers[rootStr] = string(concat)
	}

	return nil
}

// buildDirHybrid populates the info dict for a multi-file hybrid torrent.
func buildDirHybrid(infoDict *orderedDict, pieceLayers map[string]string, opts CreateOptions) error {
	fileTree := make(map[string]interface{})
	var v1Files []map[string]interface{}
	var allV1Pieces []byte

	// For v1 multi-file, we need to hash pieces across file boundaries.
	// We concatenate all file data and hash piece_length chunks with SHA-1.
	// We also collect the v2 per-file Merkle data separately.

	type fileInfo struct {
		relPath string
		absPath string
	}
	var files []fileInfo

	err := filepath.Walk(opts.Path, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil
		}
		relPath, err := filepath.Rel(opts.Path, path)
		if err != nil {
			return err
		}
		files = append(files, fileInfo{relPath: relPath, absPath: path})
		return nil
	})
	if err != nil {
		return err
	}

	if len(files) == 0 {
		return fmt.Errorf("no files found in directory: %s", opts.Path)
	}

	// Sort files for deterministic ordering
	sort.Slice(files, func(i, j int) bool {
		return files[i].relPath < files[j].relPath
	})

	// v1: SHA-1 hash across concatenated file data in piece_length chunks
	sha1Hasher := sha1.New()
	sha1BytesInPiece := 0

	for _, fi := range files {
		// v2: per-file Merkle hashing
		fr, err := hashFileHybrid(fi.absPath, opts.PieceLength)
		if err != nil {
			return err
		}

		// v2 file tree
		parts := strings.Split(filepath.ToSlash(fi.relPath), "/")
		insertFileEntry(fileTree, parts, fr.v2Entry)

		// v2 piece layers
		if len(fr.pieceLayerHash) > 0 {
			rootStr := fr.v2Entry["pieces root"].(string)
			var concat []byte
			for _, h := range fr.pieceLayerHash {
				concat = append(concat, h[:]...)
			}
			pieceLayers[rootStr] = string(concat)
		}

		// v1 files list
		pathParts := strings.Split(filepath.ToSlash(fi.relPath), "/")
		v1File := map[string]interface{}{
			"length": int(fr.size),
			"path":   pathParts,
		}
		v1Files = append(v1Files, v1File)

		// v1 piece hashing: read file and feed into running SHA-1 hasher
		f, err := os.Open(fi.absPath)
		if err != nil {
			return err
		}
		buf := make([]byte, 32*1024)
		for {
			n, err := f.Read(buf)
			if n > 0 {
				remaining := n
				offset := 0
				for remaining > 0 {
					spaceInPiece := opts.PieceLength - sha1BytesInPiece
					toWrite := remaining
					if toWrite > spaceInPiece {
						toWrite = spaceInPiece
					}
					sha1Hasher.Write(buf[offset : offset+toWrite])
					sha1BytesInPiece += toWrite
					offset += toWrite
					remaining -= toWrite

					if sha1BytesInPiece == opts.PieceLength {
						allV1Pieces = append(allV1Pieces, sha1Hasher.Sum(nil)...)
						sha1Hasher.Reset()
						sha1BytesInPiece = 0
					}
				}
			}
			if err == io.EOF {
				break
			}
			if err != nil {
				f.Close()
				return err
			}
		}
		f.Close()
	}

	// Flush last partial piece
	if sha1BytesInPiece > 0 {
		allV1Pieces = append(allV1Pieces, sha1Hasher.Sum(nil)...)
	}

	// v2 fields
	infoDict.set("file tree", fileTree)
	// v1 field: files list
	infoDict.set("files", v1Files)
	// v2 field
	infoDict.set("meta version", 2)
	// shared
	infoDict.set("name", opts.Name)
	infoDict.set("piece length", opts.PieceLength)
	// v1 field: pieces
	infoDict.set("pieces", string(allV1Pieces))
	// branding
	if opts.Source != "" {
		infoDict.set("source", opts.Source)
	}

	if opts.Private {
		infoDict.set("private", 1)
	}

	return nil
}

// hashFileHybrid reads a file and produces both v1 (SHA-1 pieces) and v2 (Merkle tree) data.
func hashFileHybrid(path string, pieceLen int) (*fileResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return nil, err
	}

	size := info.Size()

	if size == 0 {
		return &fileResult{
			size:    0,
			v2Entry: map[string]interface{}{"length": 0},
		}, nil
	}

	// Read file in 16KiB blocks for v2 Merkle tree
	// Simultaneously compute v1 SHA-1 piece hashes
	buf := make([]byte, BlockSize)
	var blockHashes [][32]byte
	var v1Pieces []byte
	sha1Hasher := sha1.New()
	bytesInPiece := 0

	for {
		n, err := io.ReadFull(f, buf)
		if n > 0 {
			// v2: SHA-256 of each 16KiB block
			blockHashes = append(blockHashes, sha256.Sum256(buf[:n]))

			// v1: SHA-1 of each piece_length chunk
			sha1Hasher.Write(buf[:n])
			bytesInPiece += n
			if bytesInPiece >= pieceLen {
				v1Pieces = append(v1Pieces, sha1Hasher.Sum(nil)...)
				sha1Hasher.Reset()
				bytesInPiece = 0
			}
		}
		if err == io.EOF || err == io.ErrUnexpectedEOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}

	// Flush last partial v1 piece
	if bytesInPiece > 0 {
		v1Pieces = append(v1Pieces, sha1Hasher.Sum(nil)...)
	}

	// v2: Build full Merkle tree from 16KiB block hashes
	tree := buildMerkleTree(blockHashes)
	root := tree[len(tree)-1][0]

	v2Entry := map[string]interface{}{
		"length":      int(size),
		"pieces root": string(root[:]),
	}

	// Extract piece layer if file spans multiple pieces
	numPieces := (int(size) + pieceLen - 1) / pieceLen
	var pieceLayerHashes [][32]byte
	if numPieces > 1 {
		pieceLayerHashes = extractPieceLayer(tree, pieceLen, numPieces)
	}

	return &fileResult{
		size:           size,
		v2Entry:        v2Entry,
		pieceLayerHash: pieceLayerHashes,
		v1PiecesSHA1:   v1Pieces,
	}, nil
}

// insertFileEntry places a file entry into the nested file tree dict.
func insertFileEntry(tree map[string]interface{}, parts []string, entry map[string]interface{}) {
	current := tree
	for i, part := range parts {
		if i == len(parts)-1 {
			current[part] = map[string]interface{}{
				"": entry,
			}
		} else {
			sub, ok := current[part]
			if !ok {
				sub = make(map[string]interface{})
				current[part] = sub
			}
			current = sub.(map[string]interface{})
		}
	}
}

// buildMerkleTree builds a complete Merkle tree from leaf hashes.
// Returns all levels: levels[0] = padded leaves, levels[len-1] = [root].
func buildMerkleTree(leaves [][32]byte) [][][32]byte {
	if len(leaves) == 0 {
		root := sha256.Sum256(make([]byte, 32))
		return [][][32]byte{{root}}
	}

	target := nextPowerOfTwo(len(leaves))
	zeroHash := sha256.Sum256(make([]byte, 32))
	padded := make([][32]byte, target)
	copy(padded, leaves)
	for i := len(leaves); i < target; i++ {
		padded[i] = zeroHash
	}

	var levels [][][32]byte
	levels = append(levels, padded)

	current := padded
	for len(current) > 1 {
		var next [][32]byte
		for i := 0; i < len(current); i += 2 {
			var combined [64]byte
			copy(combined[:32], current[i][:])
			copy(combined[32:], current[i+1][:])
			next = append(next, sha256.Sum256(combined[:]))
		}
		levels = append(levels, next)
		current = next
	}

	return levels
}

// extractPieceLayer extracts the piece layer from a Merkle tree.
func extractPieceLayer(tree [][][32]byte, pieceLen int, numPieces int) [][32]byte {
	blocksPerPiece := pieceLen / BlockSize
	level := bits.TrailingZeros(uint(blocksPerPiece))

	if level >= len(tree) {
		return nil
	}

	layer := tree[level]
	if numPieces > len(layer) {
		numPieces = len(layer)
	}

	return layer[:numPieces]
}

func isPowerOfTwo(n int) bool {
	return n > 0 && (n&(n-1)) == 0
}

func nextPowerOfTwo(n int) int {
	if isPowerOfTwo(n) {
		return n
	}
	p := 1
	for p < n {
		p <<= 1
	}
	return p
}

type orderedDict struct {
	keys   []string
	values map[string]interface{}
}

func newOrderedDict() *orderedDict {
	return &orderedDict{values: make(map[string]interface{})}
}

func (d *orderedDict) set(key string, value interface{}) {
	if _, exists := d.values[key]; !exists {
		d.keys = append(d.keys, key)
	}
	d.values[key] = value
}

func (d *orderedDict) MarshalBencode() ([]byte, error) {
	sorted := make([]string, len(d.keys))
	copy(sorted, d.keys)
	sort.Strings(sorted)

	var buf []byte
	buf = append(buf, 'd')
	for _, k := range sorted {
		keyBytes, err := bencode.EncodeBytes(k)
		if err != nil {
			return nil, err
		}
		buf = append(buf, keyBytes...)

		valBytes, err := bencode.EncodeBytes(d.values[k])
		if err != nil {
			return nil, err
		}
		buf = append(buf, valBytes...)
	}
	buf = append(buf, 'e')
	return buf, nil
}
