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
	"time"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/services/tax/internal/api"
	"github.com/BenjaminSRussell/depot/services/tax/internal/domain"
	"github.com/BenjaminSRussell/depot/services/tax/internal/provider"
	"github.com/BenjaminSRussell/depot/services/tax/internal/store"
	"github.com/BenjaminSRussell/depot/services/tax/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	// "<binary> migrate" applies the migrations as the owner role and exits: the chart's PreSync migration Job (D28).
	dbx.HandleMigrateCommand(migrations.FS)
	start := run
	switch {
	case len(os.Args) > 1 && os.Args[1] == "consume":
		start = consume
	case len(os.Args) > 1:
		slog.Error("tax stopped", "err", fmt.Errorf("unknown command %q (use no argument to serve or \"consume\" to run the Intrastat feed consumer)", os.Args[1]))
		os.Exit(2)
	}
	if err := start(context.Background(), os.Getenv); err != nil {
		slog.Error("tax stopped", "err", err)
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
	return slog.New(handler).With("service", "tax")
}

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
	providers, err := providersFromEnv(getenv)
	if err != nil {
		return fmt.Errorf("tax providers: %w", err)
	}
	st := store.New(pool)
	a := api.New(pool, providers, providerTimeout(getenv)).WithStore(st).WithConfigStore(st)
	gl, err := ledgerclient.FromEnv(getenv, "tax")
	if err != nil {
		return fmt.Errorf("ledger client: %w", err)
	}
	if gl != nil {
		a.WithLedger(gl)
	}
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", httpx.WithMetrics(a.Metrics()))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	settle, err := newSettlementJob(getenv, a, st)
	if err != nil {
		return err
	}
	switch {
	case settle != nil && gl == nil:
		slog.Warn("tax settlement job not started: it needs LEDGER_URL to reconcile against the general ledger")
	case settle != nil:
		go settle.loop(ctx)
	}
	recommit, err := newRecommitJob(getenv, a, st)
	if err != nil {
		return err
	}
	if recommit != nil {
		go recommit.loop(ctx)
	}
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "HTTP_ADDR", defaultHTTPAddr), router) }()
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	return <-errs
}

// providersFromEnv configures the external providers whose URL is set: "avalara" (AVALARA_URL and its credentials) and "vertex"
// (VERTEX_URL and its credentials). A provider with a URL but missing credentials stops the service at start rather than failing every
// calculation that names it.
func providersFromEnv(getenv func(string) string) (map[string]domain.TaxProvider, error) {
	out := map[string]domain.TaxProvider{}
	av, err := provider.AvalaraFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if av != nil {
		out[av.Name()] = av
	}
	vx, err := provider.VertexFromEnv(getenv)
	if err != nil {
		return nil, err
	}
	if vx != nil {
		out[vx.Name()] = vx
	}
	return out, nil
}

func providerTimeout(getenv func(string) string) time.Duration {
	ms, err := strconv.Atoi(getenv("TAX_PROVIDER_TIMEOUT_MS"))
	if err != nil || ms <= 0 {
		return domain.DefaultProviderTimeout
	}
	return time.Duration(ms) * time.Millisecond
}

func addr(getenv func(string) string, key, fallback string) string {
	if v := getenv(key); v != "" {
		return v
	}
	return fallback
}
