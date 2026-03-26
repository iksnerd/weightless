package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"weightless/internal/torrent"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "Usage: wl <command> [arguments]")
		fmt.Fprintln(os.Stderr, "Commands: create")
		os.Exit(1)
	}

	switch os.Args[1] {
	case "create":
		createCmd := flag.NewFlagSet("create", flag.ExitOnError)
		name := createCmd.String("name", "", "Display name for the torrent")
		tracker := createCmd.String("tracker", envOr("WL_TRACKER", "http://localhost:8080"), "Tracker base URL (or set WL_TRACKER env)")
		pieceLen := createCmd.Int("piece-length", torrent.DefaultPieceLength, "Piece length in bytes (power of 2, >= 16384)")
		apiKey := createCmd.String("api-key", "", "API key for registry authentication")
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

	announceURL := opts.trackerURL
	if !strings.Contains(announceURL, "/announce") {
		// Clean the base and append /announce
		announceURL = strings.TrimSuffix(announceURL, "/") + "/announce"
	}

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
