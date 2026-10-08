// Padboard: launchpads, side by side. Collects Stockereum and Sender activity
// from Ethereum mainnet logs, keeps an hourly snapshot on disk, and serves a
// single-page dashboard.
package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func main() {
	loadDotEnv(".env")
	key := os.Getenv("ALCHEMY_API_KEY")
	if key == "" {
		log.Fatal("ALCHEMY_API_KEY is required")
	}
	port := envOr("PORT", "8080")
	dataDir := envOr("DATA_DIR", "data")
	interval, err := time.ParseDuration(envOr("SYNC_INTERVAL", "5m"))
	if err != nil {
		log.Fatalf("SYNC_INTERVAL: %v", err)
	}

	state, err := loadState(filepath.Join(dataDir, "state.json"))
	if err != nil {
		log.Fatal(err)
	}
	httpc := &http.Client{Timeout: 90 * time.Second}
	c := &collector{
		rpc:    &rpcClient{url: "https://eth-mainnet.g.alchemy.com/v2/" + key, http: httpc},
		prices: &priceClient{base: "https://api.g.alchemy.com/prices/v1/" + key, http: httpc},
		state:  state,
	}

	go func() {
		for {
			start := time.Now()
			ctx, cancel := context.WithTimeout(context.Background(), 50*time.Minute)
			more, err := c.sync(ctx)
			if err != nil {
				log.Printf("sync: %v", err)
			}
			cancel()
			if more && err == nil {
				continue // backfill pending; the frontier was refreshed this round
			}
			if os.Getenv("SYNC_ONCE") != "" {
				os.Exit(0)
			}
			// Start syncs every interval, not interval after the last one ended.
			time.Sleep(interval - time.Since(start))
		}
	}()

	srv := &http.Server{Addr: "0.0.0.0:" + port, Handler: c.routes(), ReadHeaderTimeout: 10 * time.Second}
	log.Printf("padboard listening on :%s (data %s, sync every %s)", port, dataDir, interval)
	log.Fatal(srv.ListenAndServe())
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// loadDotEnv sets KEY=VALUE lines from a local file without overriding the environment.
func loadDotEnv(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.HasPrefix(k, "#") || os.Getenv(k) != "" {
			continue
		}
		os.Setenv(k, strings.Trim(v, `"`))
	}
}
