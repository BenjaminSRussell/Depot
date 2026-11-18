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
	"github.com/BenjaminSRussell/depot/platform/ledgerclient"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/billing/internal/api"
	"github.com/BenjaminSRussell/depot/services/billing/internal/periods"
	"github.com/BenjaminSRussell/depot/services/billing/internal/revrec"
	"github.com/BenjaminSRussell/depot/services/billing/internal/salesreport"
	"github.com/BenjaminSRussell/depot/services/billing/internal/store"
	"github.com/BenjaminSRussell/depot/services/billing/internal/taxcalc"
	"github.com/BenjaminSRussell/depot/services/billing/internal/taxreport"
	"github.com/BenjaminSRussell/depot/services/billing/migrations"
)

const (
	defaultHTTPAddr  = ":8080"
	defaultAdminAddr = ":9090"
	defaultMaxLines  = 2000
)

func main() {
	slog.SetDefault(newLogger(os.Stdout))
	// "<binary> migrate" applies the migrations as the owner role and exits: the chart's PreSync migration Job (D28).
	dbx.HandleMigrateCommand(migrations.FS)
	start := run
	if len(os.Args) > 1 && os.Args[1] == "consume" {
		start = consume
	}
	if err := start(context.Background(), os.Getenv); err != nil {
		slog.Error("billing stopped", "err", err)
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
	return slog.New(handler).With("service", "billing")
}

// run serves the billing API on HTTP_ADDR and the metrics on ADMIN_ADDR. The
// tax engine is the wave-13 tax service at TAX_URL, otherwise the fail-closed
// UnavailableTax, so no invoice posts with silent zero tax.
func run(ctx context.Context, getenv func(string) string) error {
	if err := tenant.ConfigureServiceVerifier(getenv); err != nil {
		return err
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	maxLines, err := api.ParseMaxLines(getenv("BILLING_MAX_LINES"), defaultMaxLines)
	if err != nil {
		return err
	}
	var tax api.TaxEngine = api.UnavailableTax{}
	calc, err := taxcalc.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("tax calculate client: %w", err)
	}
	if calc != nil {
		tax = calc
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
	st := store.New(pool)
	a := api.New(st, tax, maxLines).WithSubscriptions(st).WithSalesReports(st).WithTaxReports(st)
	var glProfiles map[string]store.GLProfile
	ledger, err := ledgerclient.FromEnv(getenv, "billing")
	if err != nil {
		return fmt.Errorf("ledger client: %w", err)
	}
	if ledger != nil {
		profiles, err := api.ParseGLProfiles(getenv("BILLING_GL_PROFILES"))
		if err != nil {
			return err
		}
		a.WithLedger(ledger, st, profiles)
		glProfiles = profiles
	}
	// A self-billed invoice is never posted into a closed period: close is asked, and without it nothing is posted (35-5).
	if closeURL := getenv("BILLING_CLOSE_URL"); closeURL != "" {
		signer, err := tenant.ServiceSignerFromEnv(getenv)
		if err != nil {
			return err
		}
		pc, err := periods.New(closeURL, signer, nil)
		if err != nil {
			return err
		}
		a.WithPeriods(pc)
	}
	allocations, err := revrec.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("revrec client: %w", err)
	}
	if ledger != nil && allocations == nil {
		// Every profile names deferred revenue accounts and every order line books its revrec allocation: without revrec the
		// invoice journal would hold every order invoice (BIL-0207), so the service refuses to start half configured.
		return fmt.Errorf("REVREC_URL is required when LEDGER_URL is set: order invoices book the revrec allocation")
	}
	if allocations != nil {
		a.WithAllocations(allocations)
	}
	autobook, err := newAutoBook(getenv, glProfiles, a)
	if err != nil {
		return err
	}
	subJob, err := newSubscriptionJob(getenv, a)
	if err != nil {
		return err
	}
	sales, err := salesreport.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("sales client: %w", err)
	}
	salesInterval := salesreport.DefaultInterval
	if raw := getenv("BILLING_SALES_REPORT_INTERVAL"); raw != "" {
		if salesInterval, err = time.ParseDuration(raw); err != nil || salesInterval < salesreport.MinInterval {
			return errors.New("BILLING_SALES_REPORT_INTERVAL must be a duration of at least 1s")
		}
	}
	taxClient, err := taxreport.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("tax client: %w", err)
	}
	taxInterval := salesreport.DefaultInterval
	if raw := getenv("BILLING_TAX_REPORT_INTERVAL"); raw != "" {
		if taxInterval, err = time.ParseDuration(raw); err != nil || taxInterval < salesreport.MinInterval {
			return errors.New("BILLING_TAX_REPORT_INTERVAL must be a duration of at least 1s")
		}
	}
	router := httpx.NewRouter()
	a.Register(router)
	admin := http.NewServeMux()
	var reporter *salesreport.Reporter
	if sales != nil {
		reporter = &salesreport.Reporter{Store: st, Sales: sales}
	}
	var taxReporter *taxreport.Reporter
	if taxClient != nil {
		taxReporter = &taxreport.Reporter{Store: st, Tax: taxClient}
	}
	admin.Handle("/metrics", httpx.WithMetrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.Metrics().ServeHTTP(w, r)
		if reporter != nil {
			reporter.WriteMetrics(w)
		}
		if taxReporter != nil {
			taxReporter.WriteMetrics(w)
		}
	})))

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if subJob != nil {
		go subJob.loop(ctx)
	}
	if autobook != nil {
		go autobook.loop(ctx)
	}
	if reporter != nil {
		go salesreport.Run(ctx, salesInterval, reporter.RunOnce)
		slog.Info("sales invoice reporter running", "interval", salesInterval)
	} else {
		slog.Warn("SALES_URL is unset: posted invoices are queued for sales but not reported")
	}
	if taxReporter != nil {
		go salesreport.Run(ctx, taxInterval, taxReporter.RunOnce)
		slog.Info("tax transaction reporter running", "interval", taxInterval)
	} else {
		slog.Warn("TAX_URL is unset: booked invoices and credit notes are queued for the tax service but not reported")
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
