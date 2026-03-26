package main

import (
	"bytes"
	"context"
	"crypto/hmac"
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
		createCmd.Parse(os.Args[2:])

		path := createCmd.Arg(0)
		if path == "" {
			log.Fatal("Path is required: wl create [--name NAME] <path>")
		}

		opts := createOpts{
			path:        path,
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
}

func runCreate(opts createOpts) error {
	absPath, err := filepath.Abs(opts.path)
	if err != nil {
		return fmt.Errorf("resolve path: %w", err)
	}

	if opts.name == "" {
		opts.name = filepath.Base(absPath)
	}

	announceURL := buildAnnounceURL(opts.trackerURL, opts.userID, opts.secret)

	fmt.Printf("Creating torrent for %s...\n", absPath)

	result, err := torrent.Create(torrent.CreateOptions{
		Path:        absPath,
		Name:        opts.name,
		PieceLength: opts.pieceLen,
		AnnounceURL: announceURL,
		Private:     opts.private,
		Comment:     opts.comment,
		Source:      envOr("WL_SOURCE", ""),
		CreatedBy:   envOr("WL_CREATED_BY", ""),
	})
	if err != nil {
		return fmt.Errorf("create torrent: %w", err)
	}

	// Get file/dir size for registry
	var totalSize int64
	info, err := os.Stat(absPath)
	if err == nil {
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

	// Fetch .torrent from tracker API
	torrentBytes, err := fetchTorrent(trackerBase, hash)
	if err != nil {
		return fmt.Errorf("fetch torrent: %w", err)
	}

	// Decode the torrent metadata
	meta, err := torrent.Parse(torrentBytes)
	if err != nil {
		return fmt.Errorf("decode torrent: %w", err)
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

	v1Hash, _ := hex.DecodeString(mag.InfoHashV1)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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
		TrackerURL: buildAnnounceURL(trackerBase, opts.userID, opts.secret),
		OutputDir:  outDir,
	})
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
