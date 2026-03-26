package main

import (
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"weightless/internal/tracker"
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
	go func() {
		flushTicker := time.NewTicker(10 * time.Second)
		pruneTicker := time.NewTicker(30 * time.Minute)
		for {
			select {
			case <-flushTicker.C:
				tracker.State.FlushToDB()
				tracker.State.FlushUsers()
				tracker.State.DrainBacklog()
			case <-pruneTicker.C:
				tracker.State.PruneMemory()
			}
		}
	}()

	port := os.Getenv("PORT")
	if port == "" {
		port = "8080"
	}

	http.HandleFunc("/announce/", tracker.GlobalRateLimiter.LimitMiddleware(tracker.HandleAnnounce))
	http.HandleFunc("/scrape", tracker.GlobalRateLimiter.LimitMiddleware(tracker.HandleScrape))
	http.HandleFunc("/api/registry", tracker.GlobalRateLimiter.LimitMiddleware(tracker.HandleAPI))
	http.HandleFunc("/api/registry/search", tracker.GlobalRateLimiter.LimitMiddleware(tracker.HandleSearch))
	http.HandleFunc("/api/registry/torrent", tracker.GlobalRateLimiter.LimitMiddleware(tracker.HandleTorrentDownload))
	http.HandleFunc("/health", tracker.HealthHandler)
	http.HandleFunc("/metrics", tracker.State.MetricsHandler)
	http.HandleFunc("/", tracker.IndexHandler)

	srv := &http.Server{Addr: ":" + port}

	// Graceful shutdown
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		<-sigChan
		log.Println("Shutting down... flushing final state to DB")
		tracker.State.FlushToDB()
		_ = srv.Close()
	}()

	fmt.Printf("Weightless Tracker live on :%s\n", port)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal(err)
	}
}
