// Command projects serves the Depot projects API (wave 41, slice 1).
//
// Configuration (12-factor, env):
//
//	DATABASE_URL      Postgres URL for the depot_app role (required)
//	HTTP_ADDR         listen address (default :8080)
//	MIGRATE_ON_START  "true" applies migrations at boot (dev only; stage/prod use the Argo PreSync Job)
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/services/projects/internal/api"
	"github.com/BenjaminSRussell/depot/services/projects/internal/store"
	"github.com/BenjaminSRussell/depot/services/projects/migrations"
)

func main() {
	// "<binary> migrate" applies the migrations as the owner role and exits: the chart's PreSync migration Job (D28).
	dbx.HandleMigrateCommand(migrations.FS)
	if err := run(); err != nil {
		slog.Error("projects stopped", "err", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	url := os.Getenv("DATABASE_URL")
	if url == "" {
		return errors.New("DATABASE_URL is required")
	}
	addr := os.Getenv("HTTP_ADDR")
	if addr == "" {
		addr = ":8080"
	}

	pool, err := dbx.Connect(ctx, url)
	if err != nil {
		return err
	}
	defer pool.Close()

	if os.Getenv("MIGRATE_ON_START") == "true" {
		if err := dbx.Migrate(ctx, pool, migrations.FS); err != nil {
			return err
		}
	}

	ledger, err := ledgerclient.FromEnv(os.Getenv, "projects")
	if err != nil {
		return fmt.Errorf("ledger client: %w", err)
	}
	s := store.New(pool)
	h := api.New(s)
	if ledger != nil {
		h.WithGL(s, ledger)
	}
	r := httpx.NewRouter()
	h.Register(r)
	slog.Info("projects listening", "addr", addr)
	return httpx.ServeWithMetrics(ctx, addr, r)
}
