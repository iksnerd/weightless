package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"weightless/internal/client"
	"weightless/internal/torrent"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Set via ldflags: -X main.version=... -X main.commit=... -X main.date=...
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: wl <command> [arguments]")
		fmt.Fprintln(os.Stderr, "Commands: create, get, version")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Printf("wl %s (%s) built %s\n", version, commit, date)
		return
	case "create":
		createCmd := flag.NewFlagSet("create", flag.ExitOnError)
		name := createCmd.String("name", "", "Display name for the torrent")
		tracker := createCmd.String("tracker", envOr("WL_TRACKER", "http://localhost:8080"), "Tracker base URL (or set WL_TRACKER env)")
		pieceLen := createCmd.Int("piece-length", torrent.DefaultPieceLength, "Piece length in bytes (power of 2, >= 16384)")
		apiKey := createCmd.String("api-key", "", "API key for registry authentication")
		userID := createCmd.String("user-id", envOr("WL_USER_ID", ""), "User ID for passkey auth (or set WL_USER_ID env)")
		secret := createCmd.String("secret", envOr("TRACKER_SECRET", ""), "Tracker secret for passkey signing (or set TRACKER_SECRET env)")
		private := createCmd.Bool("private", false, "Disable DHT/PEX (make weightless the sole authority)")
		description := createCmd.String("description", "", "Description of the dataset/model")
		publisher := createCmd.String("publisher", "", "Publisher or organization")
		license := createCmd.String("license", "", "License (e.g. MIT, CC-BY-4.0)")
		category := createCmd.String("category", "", "Category (e.g. models, datasets)")
		tags := createCmd.String("tags", "", "Comma-separated tags")
		comment := createCmd.String("comment", "", "Optional comment in the torrent file")
		stream := createCmd.String("stream", "", "Torrentify a remote http(s) URL without downloading it (carried as a web seed)")
		var webseeds stringSlice
		createCmd.Var(&webseeds, "webseed", "BEP 19 web seed URL (HTTP origin fallback); repeatable")
		createCmd.Parse(os.Args[2:])

		srcPath := createCmd.Arg(0)
		if srcPath == "" && *stream == "" {
			log.Fatal("Provide a path or --stream URL: wl create [--name NAME] <path> | wl create --stream <url>")
		}
		if srcPath != "" && *stream != "" {
			log.Fatal("Provide either a path or --stream, not both")
		}

		opts := createOpts{
			path:        srcPath,
			stream:      *stream,
			name:        *name,
			trackerURL:  *tracker,
			pieceLen:    *pieceLen,
			apiKey:      *apiKey,
			userID:      *userID,
			secret:      *secret,
			private:     *private,
			description: *description,
			publisher:   *publisher,
			license:     *license,
			category:    *category,
			tags:        *tags,
			comment:     *comment,
			webseeds:    webseeds,
		}

		if err := runCreate(opts); err != nil {
			log.Fatal(err)
		}
	case "get":
		getCmd := flag.NewFlagSet("get", flag.ExitOnError)
		tracker := getCmd.String("tracker", envOr("WL_TRACKER", "http://localhost:8080"), "Tracker base URL (or set WL_TRACKER env)")
		output := getCmd.String("output", "", "Output directory (default: current directory)")
		userID := getCmd.String("user-id", envOr("WL_USER_ID", ""), "User ID for passkey auth (or set WL_USER_ID env)")
		secret := getCmd.String("secret", envOr("TRACKER_SECRET", ""), "Tracker secret for passkey signing (or set TRACKER_SECRET env)")
		getCmd.Parse(os.Args[2:])

		magnetURI := getCmd.Arg(0)
		if magnetURI == "" {
			log.Fatal("Magnet link is required: wl get [--tracker URL] <magnet-link>")
		}

		opts := getOpts{
			magnetURI:  magnetURI,
			trackerURL: *tracker,
			outputDir:  *output,
			userID:     *userID,
			secret:     *secret,
		}
		if err := runGet(opts); err != nil {
			log.Fatal(err)
		}
	default:
		fmt.Fprintf(os.Stderr, "Unknown command: %s\n", os.Args[1])
		os.Exit(1)
	}
}

type createOpts struct {
	path        string
	stream      string
	name        string
	trackerURL  string
	pieceLen    int
	apiKey      string
	userID      string
	secret      string
	private     bool
	description string
	publisher   string
	license     string
	category    string
	tags        string
	comment     string
	webseeds    []string
}

// stringSlice is a repeatable string flag (e.g. --webseed a --webseed b).
type stringSlice []string

func (s *stringSlice) String() string { return strings.Join(*s, ",") }
func (s *stringSlice) Set(v string) error {
	*s = append(*s, v)
	return nil
}

func runCreate(opts createOpts) error {
	announceURL := buildAnnounceURL(opts.trackerURL, opts.userID, opts.secret)

	var result *torrent.CreateResult
	var totalSize int64
	var err error
	if opts.stream != "" {
		result, totalSize, opts.name, err = createFromStream(opts, announceURL)
	} else {
		result, totalSize, opts.name, err = createFromPath(opts, announceURL)
	}
	if err != nil {
		return err
	}

	// Write .torrent file
	outFile := opts.name + ".torrent"
	if err := os.WriteFile(outFile, result.TorrentBytes, 0644); err != nil {
		return fmt.Errorf("write torrent file: %w", err)
	}

	// Register with weightless
	// Registry is ALWAYS at /api/registry relative to the root URL
	// We extract the root URL by taking everything before /announce
	rootURL := opts.trackerURL
	if idx := strings.Index(rootURL, "/announce"); idx != -1 {
		rootURL = rootURL[:idx]
	}
	registryURL := strings.TrimSuffix(rootURL, "/") + "/api/registry"

	regBody := registryBody{
		InfoHash:    result.InfoHashHex,
		V1InfoHash:  result.InfoHashV1Hex,
		Name:        opts.name,
		Size:        totalSize,
		Description: opts.description,
		Publisher:   opts.publisher,
		License:     opts.license,
		Category:    opts.category,
		Tags:        opts.tags,
		TorrentData: result.TorrentBytes,
	}
	if err := registerHash(registryURL, regBody, opts.apiKey); err != nil {
		fmt.Fprintf(os.Stderr, "Warning: registry offline (%v). Torrent file is still valid.\n", err)
	} else {
		fmt.Printf("Registered with %s\n", registryURL)
	}

	fmt.Printf("\nTorrent: %s\n", outFile)
	fmt.Printf("Hash:    %s\n", result.InfoHashHex)
	fmt.Printf("Magnet:  %s\n", result.MagnetLink)
	return nil
}

// createFromPath builds a torrent from a local file or directory.
// Returns the result, total size, and resolved name.
func createFromPath(opts createOpts, announceURL string) (*torrent.CreateResult, int64, string, error) {
	absPath, err := filepath.Abs(opts.path)
	if err != nil {
		return nil, 0, "", fmt.Errorf("resolve path: %w", err)
	}
	name := opts.name
	if name == "" {
		name = filepath.Base(absPath)
	}

	fmt.Printf("Creating torrent for %s...\n", absPath)
	result, err := torrent.Create(torrent.CreateOptions{
		Path:        absPath,
		Name:        name,
		PieceLength: opts.pieceLen,
		AnnounceURL: announceURL,
		Private:     opts.private,
		Comment:     opts.comment,
		Source:      envOr("WL_SOURCE", ""),
		CreatedBy:   envOr("WL_CREATED_BY", ""),
		WebSeeds:    opts.webseeds,
	})
	if err != nil {
		return nil, 0, "", fmt.Errorf("create torrent: %w", err)
	}

	var totalSize int64
	if info, err := os.Stat(absPath); err == nil {
		if info.IsDir() {
			filepath.Walk(absPath, func(_ string, fi os.FileInfo, _ error) error {
				if fi != nil && !fi.IsDir() {
					totalSize += fi.Size()
				}
				return nil
			})
		} else {
			totalSize = info.Size()
		}
	}
	return result, totalSize, name, nil
}

// createFromStream torrentifies a remote HTTP origin without downloading it to
// disk: it streams the body once to hash it, and carries the origin URL as a
// web seed so clients can fetch from it. Requires a known Content-Length.
func createFromStream(opts createOpts, announceURL string) (*torrent.CreateResult, int64, string, error) {
	streamURL := opts.stream
	u, err := url.Parse(streamURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, 0, "", fmt.Errorf("--stream must be an http(s) URL, got %q", streamURL)
	}
	name := opts.name
	if name == "" {
		name = path.Base(u.Path)
	}
	if name == "" || name == "." || name == "/" {
		return nil, 0, "", fmt.Errorf("could not derive a name from %q; pass --name", streamURL)
	}

	fmt.Printf("Streaming and hashing %s...\n", streamURL)
	resp, err := http.Get(streamURL)
	if err != nil {
		return nil, 0, "", fmt.Errorf("fetch stream: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, "", fmt.Errorf("stream origin returned status %d", resp.StatusCode)
	}
	if resp.ContentLength <= 0 {
		return nil, 0, "", fmt.Errorf("stream origin did not report a Content-Length; cannot hash without a known size")
	}

	// The origin is itself a web seed; add it (deduped) to any explicit ones.
	webseeds := append([]string{streamURL}, opts.webseeds...)

	result, err := torrent.CreateStream(torrent.CreateOptions{
		Name:        name,
		PieceLength: opts.pieceLen,
		AnnounceURL: announceURL,
		Private:     opts.private,
		Comment:     opts.comment,
		Source:      envOr("WL_SOURCE", ""),
		CreatedBy:   envOr("WL_CREATED_BY", ""),
		WebSeeds:    webseeds,
	}, resp.Body, resp.ContentLength, name)
	if err != nil {
		return nil, 0, "", fmt.Errorf("create torrent from stream: %w", err)
	}
	return result, resp.ContentLength, name, nil
}

type registryBody struct {
	InfoHash    string `json:"info_hash"`
	V1InfoHash  string `json:"v1_info_hash,omitempty"`
	Name        string `json:"name"`
	Size        int64  `json:"size,omitempty"`
	Description string `json:"description,omitempty"`
	Publisher   string `json:"publisher,omitempty"`
	License     string `json:"license,omitempty"`
	Category    string `json:"category,omitempty"`
	Tags        string `json:"tags,omitempty"`
	TorrentData []byte `json:"torrent_data,omitempty"`
}

type getOpts struct {
	magnetURI  string
	trackerURL string
	outputDir  string
	userID     string
	secret     string
}

func runGet(opts getOpts) error {
	mag, err := torrent.ParseMagnet(opts.magnetURI)
	if err != nil {
		return fmt.Errorf("parse magnet: %w", err)
	}

	hash := mag.BestHash()
	name := mag.DisplayName
	if name == "" {
		name = hash[:16]
	}

	fmt.Printf("Fetching metadata for %s...\n", name)
	if mag.InfoHashV1 != "" {
		fmt.Printf("  v1 hash: %s\n", mag.InfoHashV1)
	}
	if mag.InfoHashV2 != "" {
		fmt.Printf("  v2 hash: %s\n", mag.InfoHashV2)
	}

	// Resolve tracker URL: prefer magnet tr param, fall back to --tracker flag
	trackerBase := opts.trackerURL
	if len(mag.Trackers) > 0 {
		// Extract base URL from announce URL
		tr := mag.Trackers[0]
		if idx := strings.Index(tr, "/announce"); idx != -1 {
			trackerBase = tr[:idx]
		} else {
			trackerBase = tr
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Metadata: try the registry first, then fall back to BEP 9 peer exchange
	// (so a magnet resolves even when the tracker has no registry entry).
	announceURL := buildAnnounceURL(trackerBase, opts.userID, opts.secret)
	torrentBytes, meta, err := acquireMetadata(ctx, trackerBase, announceURL, mag)
	if err != nil {
		return err
	}

	// Determine output directory
	outDir := opts.outputDir
	if outDir == "" {
		outDir = "."
	}

	// Save .torrent file
	torrentFile := filepath.Join(outDir, name+".torrent")
	if err := os.WriteFile(torrentFile, torrentBytes, 0644); err != nil {
		return fmt.Errorf("write torrent file: %w", err)
	}

	// Print file listing
	fmt.Printf("\nTorrent: %s\n", torrentFile)
	fmt.Printf("Pieces:  %d x %s\n", meta.PieceCount, formatBytes(int64(meta.PieceLength)))
	fmt.Printf("Total:   %s\n", formatBytes(meta.TotalSize))
	if len(meta.Files) == 1 {
		fmt.Printf("File:    %s\n", meta.Files[0].Path)
	} else {
		fmt.Printf("Files:   %d\n", len(meta.Files))
		for _, f := range meta.Files {
			fmt.Printf("  %s  %s\n", formatBytes(f.Length), f.Path)
		}
	}

	// Invoke Stage C Downloader
	clientFiles := make([]client.FileEntry, len(meta.Files))
	for i, f := range meta.Files {
		clientFiles[i] = client.FileEntry{Path: f.Path, Length: f.Length}
	}

	// A v2-only magnet has no v1 hash, which is fine; but if one is present it
	// must be valid hex — reject a malformed value at the boundary rather than
	// handing a truncated hash to the downloader.
	v1Hash, err := hex.DecodeString(mag.InfoHashV1)
	if err != nil {
		return fmt.Errorf("invalid v1 info hash %q: %w", mag.InfoHashV1, err)
	}

	return client.DownloadMVP(ctx, client.DownloadOptions{
		Meta: client.TorrentMeta{
			Name:        meta.Name,
			InfoHashV1:  v1Hash,
			PieceLength: meta.PieceLength,
			PieceCount:  meta.PieceCount,
			TotalSize:   meta.TotalSize,
			Pieces:      meta.Pieces,
			Files:       clientFiles,
		},
		TrackerURL: announceURL,
		OutputDir:  outDir,
	})
}

// acquireMetadata returns the torrent bytes and parsed metadata, trying the
// registry API first and falling back to BEP 9 peer metadata exchange when the
// registry has no entry for the hash.
func acquireMetadata(ctx context.Context, trackerBase, announceURL string, mag torrent.Magnet) ([]byte, torrent.TorrentMeta, error) {
	if tb, err := fetchTorrent(trackerBase, mag.BestHash()); err == nil {
		if meta, perr := torrent.Parse(tb); perr == nil {
			return tb, meta, nil
		}
	}

	// Registry miss — fall back to peers. BEP 9 ut_metadata verifies against the
	// v1 (SHA-1) info hash, so we need one.
	fmt.Println("  registry has no entry; fetching metadata from peers (BEP 9)...")
	if mag.InfoHashV1 == "" {
		return nil, torrent.TorrentMeta{}, fmt.Errorf("not in registry and magnet has no v1 hash; cannot fetch metadata from peers")
	}
	v1Hash, err := hex.DecodeString(mag.InfoHashV1)
	if err != nil || len(v1Hash) != 20 {
		return nil, torrent.TorrentMeta{}, fmt.Errorf("invalid v1 info hash %q", mag.InfoHashV1)
	}

	peerID := newPeerID()
	addrs, err := client.Announce(ctx, announceURL, string(v1Hash), peerID, 6881, 0)
	if err != nil {
		return nil, torrent.TorrentMeta{}, fmt.Errorf("announce for peers: %w", err)
	}
	if len(addrs) == 0 {
		return nil, torrent.TorrentMeta{}, fmt.Errorf("no peers available to fetch metadata from")
	}

	infoBytes, err := fetchMetadataFromPeers(ctx, addrs, v1Hash, peerID)
	if err != nil {
		return nil, torrent.TorrentMeta{}, err
	}
	meta, err := torrent.ParseInfo(infoBytes)
	if err != nil {
		return nil, torrent.TorrentMeta{}, fmt.Errorf("parse peer metadata: %w", err)
	}
	tb, err := torrent.BuildMetainfo(infoBytes, announceURL)
	if err != nil {
		return nil, torrent.TorrentMeta{}, fmt.Errorf("rebuild torrent: %w", err)
	}
	return tb, meta, nil
}

// fetchMetadataFromPeers connects to peers in turn and returns the first info
// dict successfully fetched via BEP 9.
func fetchMetadataFromPeers(ctx context.Context, addrs []string, v1Hash []byte, peerID string) ([]byte, error) {
	var lastErr error
	for _, addr := range addrs {
		p, err := client.Connect(ctx, addr)
		if err != nil {
			lastErr = err
			continue
		}
		if err := p.Handshake(ctx, v1Hash, peerID); err != nil {
			p.Close()
			lastErr = err
			continue
		}
		if p.MetadataSize <= 0 {
			p.Close()
			lastErr = fmt.Errorf("%s did not advertise metadata_size", addr)
			continue
		}
		data, err := p.FetchMetadata(ctx, v1Hash)
		p.Close()
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	return nil, fmt.Errorf("no peer served metadata (%d tried): %w", len(addrs), lastErr)
}

// newPeerID returns a 20-byte BEP 20 peer id.
func newPeerID() string {
	b := make([]byte, 12)
	if _, err := rand.Read(b); err != nil {
		return "-WL0020-aaaaaaaaaaaa"
	}
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	for i := range b {
		b[i] = charset[int(b[i])%len(charset)]
	}
	return "-WL0020-" + string(b)
}

// buildAnnounceURL constructs the announce URL, optionally with a signed passkey path.
func buildAnnounceURL(trackerBase, userID, secret string) string {
	base := strings.TrimSuffix(trackerBase, "/")
	if userID != "" && secret != "" {
		passkey := userID + "." + signUserID(userID, secret)
		return base + "/announce/" + passkey
	}
	return base + "/announce"
}

// signUserID generates an HMAC-SHA256 signature matching the tracker's auth.
func signUserID(userID, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(userID))
	return hex.EncodeToString(mac.Sum(nil))
}

// fetchTorrent downloads the .torrent binary from the tracker registry API.
func fetchTorrent(trackerBase, infoHash string) ([]byte, error) {
	u := strings.TrimSuffix(trackerBase, "/") + "/api/registry/torrent?info_hash=" + url.QueryEscape(infoHash)

	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(u)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, fmt.Errorf("torrent not found in registry (hash: %s)", infoHash)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("registry returned %d", resp.StatusCode)
	}

	return io.ReadAll(resp.Body)
}

func formatBytes(b int64) string {
	switch {
	case b >= 1<<30:
		return fmt.Sprintf("%.1f GB", float64(b)/float64(1<<30))
	case b >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(b)/float64(1<<20))
	case b >= 1<<10:
		return fmt.Sprintf("%.1f KB", float64(b)/float64(1<<10))
	default:
		return fmt.Sprintf("%d B", b)
	}
}

func registerHash(registryURL string, body registryBody, apiKey string) error {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", registryURL, bytes.NewReader(jsonBody))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-Weightless-Key", apiKey)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		return fmt.Errorf("registry returned %d", resp.StatusCode)
	}
	return nil
}
