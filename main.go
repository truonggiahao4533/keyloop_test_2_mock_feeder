// Command mock-feed acts like a dealer's inventory system: it posts random
// vehicle batches to the inventory API's ingest endpoint.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

// devSecret is the JWT secret KL_Test's docker-compose.yml uses by default.
const devSecret = "dev-secret-change-me-please"

func main() {
	if err := run(); err != nil {
		slog.Error("stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	url := flag.String("url", getenv("API_URL", "http://localhost:8080"), "API base URL")
	interval := flag.Duration("interval", 30*time.Second, "time between batches")
	maxBatch := flag.Int("batch", 20, "largest batch size, 1 to 1000")
	count := flag.Int("count", 0, "batches to send, 0 runs until stopped")
	seed := flag.Uint64("seed", 0, "random seed, 0 picks one; the same seed repeats the same VINs")
	dryRun := flag.Bool("dry-run", false, "print batches instead of sending them")
	flag.Parse()

	if *maxBatch < 1 || *maxBatch > 1000 {
		return errors.New("-batch must be between 1 and 1000")
	}
	if *interval <= 0 {
		return errors.New("-interval must be positive")
	}
	if *count < 0 {
		return errors.New("-count must not be negative")
	}
	if *seed == 0 {
		*seed = uint64(time.Now().UnixNano())
	}

	client := &Client{
		BaseURL: *url,
		HTTP:    &http.Client{Timeout: 10 * time.Second},
		Token:   tokenSource(),
	}
	gen := NewGenerator(*seed, *maxBatch, time.Now)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	slog.Info("feed started", "url", *url, "interval", interval.String(), "max_batch", *maxBatch, "seed", *seed)
	failed := 0
	for sent := 0; *count == 0 || sent < *count; sent++ {
		if sent > 0 {
			select {
			case <-ctx.Done():
				slog.Info("feed stopped")
				return nil
			case <-time.After(*interval):
			}
		}
		batch := gen.Next()
		if *dryRun {
			if err := printBatch(batch); err != nil {
				return err
			}
			gen.Commit(batch)
			continue
		}
		res, err := client.Post(ctx, batch.Vehicles)
		switch {
		case err == nil:
			gen.Commit(batch)
			slog.Info("batch accepted", "sent", len(batch.Vehicles), "new", batch.New,
				"repriced", batch.Repriced, "left_stock", batch.Left,
				"inserted", res.Inserted, "updated", res.Updated)
		case ctx.Err() != nil:
			slog.Info("feed stopped")
			return nil
		case errors.Is(err, errAuth):
			return fmt.Errorf("%w; check that JWT_SECRET or INGEST_TOKEN matches the API", err)
		default:
			// The batch is dropped and nothing is remembered from it, so the
			// feed keeps going while the API is down or rate limiting.
			failed++
			slog.Warn("batch failed", "sent", len(batch.Vehicles), "err", err)
		}
	}
	if failed > 0 {
		return fmt.Errorf("%d of %d batches failed", failed, *count)
	}
	return nil
}

// tokenSource uses INGEST_TOKEN if set. Otherwise it mints a fresh token for
// each batch, so a long run never sends an expired one.
func tokenSource() func() (string, error) {
	if token := os.Getenv("INGEST_TOKEN"); token != "" {
		return func() (string, error) { return token, nil }
	}
	secret := getenv("JWT_SECRET", devSecret)
	return func() (string, error) { return mintToken(secret, "mock-feed", time.Now()) }
}

func printBatch(b Batch) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(struct {
		Vehicles []Vehicle `json:"vehicles"`
	}{b.Vehicles})
}

func getenv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}
