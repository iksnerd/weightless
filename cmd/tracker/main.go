package main

import (
	"context"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus/promhttp"
	"weightless/internal/tracker"
)

// Set via ldflags: -X main.version=... -X main.commit=... -X main.date=...
var (
	version = "dev"
	commit  = "unknown"
	date    = "unknown"
)

func main() {
	// Initialize configuration and database
	tracker.InitConfig()
	tracker.MustOpenDB()
	tracker.InitSchema()

	// Initialize in-memory state
	if err := tracker.State.LoadFromDB(); err != nil {
		log.Fatalf("Failed to load state: %v", err)
	}

	// Periodically flush memory state to SQLite (every 10s)
	// and prune stale peers from memory (every 30m)
	done := make(chan struct{})
	go func() {
		flushTicker := time.NewTicker(10 * time.Second)
		pruneTicker := time.NewTicker(30 * time.Minute)
		defer flushTicker.Stop()
		defer pruneTicker.Stop()
		for {
			select {
			case <-flushTicker.C:
				tracker.State.FlushToDB()
				tracker.State.FlushUsers()
				tracker.State.DrainBacklog()
			case <-pruneTicker.C:
				tracker.State.PruneMemory()
			case <-done:
				return
			}
		}
	}()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/announce", tracker.GlobalRateLimiter.LimitMiddleware(tracker.HandleAnnounce))
	http.HandleFunc("/announce/", tracker.GlobalRateLimiter.LimitMiddleware(tracker.HandleAnnounce))
	http.HandleFunc("/scrape", tracker.GlobalRateLimiter.LimitMiddleware(tracker.HandleScrape))
	http.HandleFunc("/api/registry", tracker.GlobalRateLimiter.LimitMiddleware(tracker.HandleAPI))
	http.HandleFunc("/api/registry/search", tracker.GlobalRateLimiter.LimitMiddleware(tracker.HandleSearch))
	http.HandleFunc("/api/registry/meta", tracker.GlobalRateLimiter.LimitMiddleware(tracker.HandleMetadata))
	http.HandleFunc("/api/registry/torrent", tracker.GlobalRateLimiter.LimitMiddleware(tracker.HandleTorrentDownload))
	http.HandleFunc("/health", tracker.HealthHandler)
	http.Handle("/metrics", promhttp.Handler())
	http.HandleFunc("/", tracker.IndexHandler)

	srv := &http.Server{Addr: ":" + port}

	// Graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		<-sigChan
		log.Println("Shutting down... stopping tickers and flushing final state to DB")
		close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		// Stop accepting new announces first so the final flush captures all state,
		// then mirror the periodic flusher (state + users + backlog).
		if err := srv.Shutdown(ctx); err != nil {
			log.Printf("Shutdown error: %v", err)
		}
		tracker.State.FlushToDB()
		tracker.State.FlushUsers()
		tracker.State.DrainBacklog()
	}()

	fmt.Printf("Weightless Tracker %s (%s) live on :%s\n", version, commit, port)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
