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
	tickerStopped := make(chan struct{})
	go func() {
		defer close(tickerStopped)
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

	// Expose the ldflags build version via the index page.
	tracker.Version = version

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

	// Timeouts guard against slowloris-style connections holding workers open.
	// Announce/scrape/API requests are all small; the only large request is the
	// registry POST, whose torrent_data is capped at 100MB, so 30s read/write
	// is sufficient.
	srv := &http.Server{
		Addr:              ":" + port,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	// Graceful shutdown. srv.Shutdown closes the listeners first, which makes
	// ListenAndServe below return ErrServerClosed immediately; shutdownDone
	// keeps main alive until the final flush has completed.
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		<-sigChan
		log.Println("Shutting down... stopping tickers and flushing final state to DB")
		// Wait for the periodic goroutine to fully exit before running the final
		// flush below. Without this, a tick already mid-flush when done closes
		// could still be sending FlushUsers' Hub usage-sync POST when the final
		// flush starts its own; FlushUsers releases its lock before the HTTP
		// call, so both would snapshot and report the same usage delta to the
		// Hub before either subtracts it, double-reporting it upstream.
		close(done)
		<-tickerStopped
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
	// Listeners are closed; wait for the signal goroutine to finish flushing.
	<-shutdownDone
}
