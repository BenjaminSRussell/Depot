// Command transport serves the wave-34 transportation API: rate cards, rate
// shopping, freight bills, carrier invoices, tenders, tracking, landed-cost voyages,
// loads, the gate, screening holds, route optimization and load plans.
// ROUTING_PY_URL points at services/routing-py (the peer-URL name depot-server sets;
// TMS_OPTIMIZER_URL is the deprecated older name and is still read); TMS_RATING_CACHE_MIN
// sets the rate-shop cache window.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strconv"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/transport/internal/api"
	"github.com/BenjaminSRussell/depot/services/transport/internal/optimizer"
	"github.com/BenjaminSRussell/depot/services/transport/internal/store"
	"github.com/BenjaminSRussell/depot/services/transport/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	// "<binary> migrate" applies the migrations as the owner role and exits: the chart's PreSync migration Job (D28).
	dbx.HandleMigrateCommand(migrations.FS)
	if err := run(context.Background(), os.Getenv); err != nil {
		slog.Error("transport stopped", "err", err)
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
	return slog.New(handler).With("service", "transport")
}

func run(ctx context.Context, getenv func(string) string) error {
	// DEPOT_SERVICE_NAME and DEPOT_SERVICE_PUBLIC_KEYS admit workflow-ts's
	// signed tender calls (transport.tender.execute); a malformed key list stops
	// the service here instead of rejecting every service token.
	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
		return err
	}
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
	if err := tenant.ShareReplayCache(ctx, pool); err != nil {
		return err
	}
	a := api.New(store.New(pool))
	if u := optimizerURL(getenv, slog.Default()); u != "" {
		a.WithOptimizer(optimizer.New(u, nil))
	}
	if v := getenv("TMS_RATING_CACHE_MIN"); v != "" {
		m, err := strconv.Atoi(v)
		if err != nil {
			return fmt.Errorf("TMS_RATING_CACHE_MIN: %w", err)
		}
		a.WithRatingCacheMinutes(m)
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

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}

// optimizerURL is the routing-py base URL: ROUTING_PY_URL, the name every deployment style uses for a peer service, else the
// deprecated TMS_OPTIMIZER_URL (logged as deprecated). ROUTING_PY_URL wins when both are set.
func optimizerURL(getenv func(string) string, log *slog.Logger) string {
	if u := getenv("ROUTING_PY_URL"); u != "" {
		return u
	}
	if u := getenv("TMS_OPTIMIZER_URL"); u != "" {
		log.Warn("TMS_OPTIMIZER_URL is deprecated; set ROUTING_PY_URL instead", "variable", "TMS_OPTIMIZER_URL")
		return u
	}
	return ""
}
