// Command eam runs the Enterprise Asset Management API (wave 44).
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/services/eam/internal/api"
	"github.com/BenjaminSRussell/depot/services/eam/internal/store"
	"github.com/BenjaminSRussell/depot/services/eam/migrations"
)

func main() {
	// "<binary> migrate" applies the migrations as the owner role and exits: the chart's PreSync migration Job (D28).
	dbx.HandleMigrateCommand(migrations.FS)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Getenv, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "eam:", err)
		os.Exit(1)
	}
}

// run wires the service from its environment. Configuration errors exit non-zero.
func run(ctx context.Context, getenv func(string) string, out io.Writer) error {
	logger := newLogger(out, serviceName(getenv))
	dsn := getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is required")
	}
	addr := getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}
	pool, err := dbx.Connect(ctx, dsn)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer pool.Close()
	// Dev only: outside dev the chart's migration Job applies them as the owner role (D28).
	if getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return fmt.Errorf("migrate: %w", err)
		}
	}
	s := store.New(pool)
	h := api.New(s, s.Ready)
	ledger, err := ledgerclient.FromEnv(getenv, "eam")
	if err != nil {
		return fmt.Errorf("ledger client: %w", err)
	}
	if ledger != nil {
		h.WithLedger(ledger)
	}
	logger.Info("listening", "addr", addr)
	return httpx.ServeWithMetrics(ctx, addr, h.Routes())
}

func serviceName(getenv func(string) string) string {
	if n := getenv("SERVICE_NAME"); n != "" {
		return n
	}
	return "eam"
}

// newLogger returns a JSON logger with the required service field (kernel logging rules).
func newLogger(w io.Writer, service string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(w, nil)).With("service", service)
}
