package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"time"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/print/internal/api"
	"github.com/BenjaminSRussell/depot/services/print/internal/config"
	"github.com/BenjaminSRussell/depot/services/print/internal/delivery"
	"github.com/BenjaminSRussell/depot/services/print/internal/jobs"
	"github.com/BenjaminSRussell/depot/services/print/internal/statements"
	"github.com/BenjaminSRussell/depot/services/print/internal/store"
	"github.com/BenjaminSRussell/depot/services/print/internal/transport"
	"github.com/BenjaminSRussell/depot/services/print/migrations"
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
		slog.Error("print stopped", "err", err)
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
	return slog.New(handler).With("service", "print")
}

func run(ctx context.Context, getenv func(string) string) error {
	cfg, err := config.Load(getenv)
	if err != nil {
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
	st := store.New(pool)
	sender := transport.Router{TCP: transport.TCP{Timeout: 2 * time.Second}, IPP: transport.IPP{Timeout: 2 * time.Second}}
	var mailer delivery.Mailer = delivery.LogMailer{Log: slog.Default()}
	if cfg.SMTPAddr != "" {
		mailer = delivery.SMTPMailer{Addr: cfg.SMTPAddr, From: cfg.SMTPFrom, Username: cfg.SMTPUsername, Password: cfg.SMTPPassword, RequireTLS: cfg.SMTPRequireTLS}
	}
	signer, err := tenant.ServiceSignerFromEnv(getenv)
	if err != nil {
		return err
	}
	opts := []api.Option{api.WithAttemptsPerDevice(cfg.AttemptsPerDevice()), api.WithRetentionDays(cfg.RetentionDays), api.WithMailer(mailer), api.WithStatementTenants(cfg.StatementTenantIDs)}
	var source statements.Source
	if cfg.ReceivablesURL != "" && signer != nil {
		if source, err = statements.NewHTTPSource(cfg.ReceivablesURL, cfg.PartyURL, signer, nil); err != nil {
			return err
		}
	}
	if hs, ok := source.(*statements.HTTPSource); ok {
		opts = append(opts, api.WithAccountChecker(hs))
	}
	a := api.New(st, sender, opts...)
	var runner *statements.Runner
	if source != nil {
		runner = &statements.Runner{Schedules: st, Docs: st, Source: source, Deliver: a.Deliverer()}
		a.SetStatementRunner(runner)
	}
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	admin.Handle("/metrics", httpx.WithMetrics(a.Metrics()))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if len(cfg.PurgeTenantIDs) > 0 {
		go jobs.RunDaily(ctx, st, cfg.PurgeTenantIDs, cfg.RetentionDays, func(deleted int64, err error) {
			a.RecordPurged(deleted)
			if err != nil {
				slog.Error("scheduled job-log purge failed", "err", err, "deleted", deleted)
				return
			}
			slog.Info("scheduled job-log purge", "deleted", deleted, "tenants", len(cfg.PurgeTenantIDs))
		})
		go jobs.RunSweep(ctx, a, cfg.PurgeTenantIDs, jobs.SweepInterval, func(claimed int, err error) {
			if err != nil {
				slog.Error("scheduled queue sweep failed", "err", err, "claimed", claimed)
				return
			}
			slog.Info("scheduled queue sweep", "claimed", claimed)
		})
	} else {
		slog.Info("scheduled job-log purge disabled", "reason", "PRINT_PURGE_TENANT_IDS is empty")
	}
	if runner != nil && len(cfg.StatementTenantIDs) > 0 {
		go statements.RunLoop(ctx, runner, cfg.StatementTenantIDs, statements.Interval, func(sums []statements.Summary, err error) {
			if err != nil {
				slog.Error("scheduled statements failed", "err", err)
			}
			for _, s := range sums {
				slog.Info("scheduled statements", "schedule", s.ScheduleID, "asOf", s.AsOf, "delivered", s.Delivered, "skipped", s.Skipped, "failed", s.Failed, "retry", s.Retry)
			}
		})
	} else if runner != nil {
		slog.Warn("scheduled statements are not running: PRINT_STATEMENT_TENANT_IDS is empty; schedules are stored but only run now runs them")
	}
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
