package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/eventing/kafka"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
	"github.com/BenjaminSRussell/passage/services/tax/internal/feed"
	"github.com/BenjaminSRussell/passage/services/tax/internal/store"
)

func intrastatKafkaConfig(getenv func(string) string) (kafka.Config, error) {
	return kafka.ConfigFromEnv(getenv, feed.ConsumerName, feed.ConsumerName, []string{feed.PackingSlipTopic, feed.ReceiptTopic})
}

// intrastatSeeded reports whether the service principal table gives the consumer its principal with the feed duty; the consumer never
// borrows another consumer's principal.
func intrastatSeeded(table map[string][]string) bool {
	return slices.Contains(table[feed.ConsumerName], "tax.intrastat.feed")
}

// consume runs the Intrastat feed consumer (#13): it stages the EU cross-border movements of fulfillment's packingslip.posted and
// receiving's receipt.posted through the store behind POST /v1/tax/intrastat/movements, one transaction per event with its consumer_inbox
// row, and parks what it cannot map on passage.tax.intrastat.dlq. It serves only its admin port.
func consume(ctx context.Context, getenv func(string) string) error {
	if !intrastatSeeded(tenant.ServicePrincipalTable()) {
		return errors.New("Intrastat consumer not started: service principal " + tenant.ServiceActor(feed.ConsumerName) + " is not seeded")
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	cfg, err := intrastatKafkaConfig(getenv)
	if err != nil {
		return err
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	client, err := kafka.New(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	// Not Idempotent: the inbox row and the staged movement commit in one transaction.
	consumer, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), eventing.Config{
		Name: feed.ConsumerName, Handlers: feed.Handlers(store.New(pool)), DeadLetterTopic: feed.DeadLetterTopic, Log: slog.Default(),
	})
	if err != nil {
		return err
	}
	admin := http.NewServeMux()
	admin.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	admin.HandleFunc("/readyz", func(w http.ResponseWriter, r *http.Request) {
		if err := client.Ping(r.Context()); err != nil {
			slog.ErrorContext(r.Context(), "kafka not reachable", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	admin.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		consumer.Metrics().Write(w, feed.ConsumerName)
	})

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	go func() {
		err := consumer.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", feed.ConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}
