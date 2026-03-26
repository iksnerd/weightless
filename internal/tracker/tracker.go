package tracker

import (
	"bufio"
	"fmt"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"

	_ "modernc.org/sqlite" // Pure Go SQLite (Zero-CGO)
)

var (
	MaxPeers          = 50
	GlobalRateLimiter = NewRateLimiter(5.0, 10.0, 10000) // 5 req/sec, 10 burst
)

func loadEnv() {
	// Try current dir and then /usr/local/bin/ (for Docker)
	paths := []string{".env.local", "/usr/local/bin/.env.local"}
	var f *os.File
	var err error

	found := ""
	for _, p := range paths {
		f, err = os.Open(p)
		if err == nil {
			found = p
			break
		}
	}

	if f == nil {
		log.Println("WARNING: No .env.local found. Environment variables must be set manually.")
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	count := 0
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			key := strings.TrimSpace(parts[0])
			val := strings.TrimSpace(parts[1])
			if os.Getenv(key) == "" {
				os.Setenv(key, val)
				count++
			}
		}
	}
	log.Printf("Loaded %d variables from %s", count, found)
}

func InitConfig() {
	loadEnv()

	if mp := os.Getenv("MAX_PEERS"); mp != "" {
		if n, err := strconv.Atoi(mp); err == nil && n > 0 {
			MaxPeers = n
		}
	}
}

func IndexHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	if _, err := fmt.Fprint(w, "Weightless Tracker v1.0"); err != nil {
	}
}

func HealthHandler(w http.ResponseWriter, _ *http.Request) {
	if err := DB.Ping(); err != nil {
		http.Error(w, "Database unreachable", http.StatusServiceUnavailable)
		return
	}
	if _, err := fmt.Fprint(w, "OK"); err != nil {
	}
}
