package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/api"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/partyclient"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/store"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/taxclient"
	"github.com/BenjaminSRussell/depot/services/intercompany/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	if len(os.Args) > 1 && os.Args[1] == reconcileCommand {
		if err := reconcileJob(context.Background(), os.Getenv, os.Args[2:]); err != nil {
			slog.Error("intercompany reconcile stopped", "err", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == consumeCommand {
		if err := consume(context.Background(), os.Getenv); err != nil {
			slog.Error("intercompany consume stopped", "err", err)
			os.Exit(1)
		}
		return
	}
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("intercompany stopped", "err", err)
		os.Exit(1)
	}
}

func newLogger(w io.Writer) *slog.Logger {
	renamed := map[string]string{slog.TimeKey: "timestamp", slog.MessageKey: "message"}
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
		if name, ok := renamed[a.Key]; ok {
			a.Key = name
		}
		return a
	}})
	return slog.New(handler).With("service", "intercompany")
}

// run connects to DATABASE_URL, applies the migrations when MIGRATE_ON_START
// is true (dev; stage and prod run them before the rollout), and serves the
// API on HTTP_ADDR and /metrics on ADMIN_ADDR. LEDGER_URL, with the service
// signing key, turns on posting of journals and charges to the ledgers and the
// reconciliation that reads them. FX_URL (rate type FX_RATE_TYPE, default
// closing) converts pairs in two currencies, ORG_URL finds the first open period
// after a closed one by the fiscal calendar, TAX_URL prices cross-border charges and PARTY_URL checks a pairing's customer and vendor accounts.
func run(ctx context.Context, getenv func(string) string) error {
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	if getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	st := store.New(pool)
	a := api.New(st).WithReconciliation(st).WithRegistry(st)
	rec, err := buildReconciler(getenv, st)
	if err != nil {
		return err
	}
	if rec.ledger != nil {
		a.WithPosting(rec.poster, st, st)
		a.WithLedgerReconciliation(rec.reconciler)
	}
	taxes, err := taxclient.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("tax client: %w", err)
	}
	if taxes != nil {
		a.WithTax(taxes)
	}
	parties, err := partyclient.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("party client: %w", err)
	}
	if parties != nil {
		a.WithParty(parties)
	}
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", httpx.WithMetrics(a.Metrics()))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), router) }()
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	return <-errs
}

func addr(getenv func(string) string, name, fallback string) string {
	if v := getenv(name); v != "" {
		return v
	}
	return fallback
}
