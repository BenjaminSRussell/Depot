package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/eventing/kafka"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/api"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/chargefeed"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/store"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/taxclient"
)

const consumeCommand = "consume"

// chargeFeedSeeded reports whether the service principal table gives the consumer its principal with the one flag duty.
func chargeFeedSeeded(table map[string][]string) bool {
	return slices.Contains(table[chargefeed.ConsumerName], chargefeed.Duty)
}

// chargeFeedConfig reads the accounts the Journal-mode legs of a fed charge post to: IC_CHARGE_COST_ACCOUNT, IC_CHARGE_RECOVERY_ACCOUNT
// and IC_CHARGE_MARGIN_ACCOUNT (all required).
func chargeFeedConfig(getenv func(string) string) (chargefeed.Config, error) {
	cfg := chargefeed.Config{CostAccount: getenv("IC_CHARGE_COST_ACCOUNT"), RecoveryAccount: getenv("IC_CHARGE_RECOVERY_ACCOUNT"), MarginAccount: getenv("IC_CHARGE_MARGIN_ACCOUNT")}
	return cfg, cfg.Validate()
}

// consume is `intercompany consume` (#40): it raises an intercompany charge for every expense line and every project hour that
// another legal entity bears, from expense's report.approved and projects' transaction.posted, through the same code as the charge
// route (transfer prices, tax, settlement mode, the ledger poster). Each charge is stored per source and version, so a redelivered
// event changes nothing. It serves only its admin port.
func consume(ctx context.Context, getenv func(string) string) error {
	if !chargeFeedSeeded(tenant.ServicePrincipalTable()) {
		return errors.New("ic charge feed not started: service principal " + tenant.ServiceActor(chargefeed.ConsumerName) + " is not seeded")
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	cfg, err := chargeFeedConfig(getenv)
	if err != nil {
		return err
	}
	kcfg, err := kafka.ConfigFromEnv(getenv, chargefeed.ConsumerName, chargefeed.ConsumerName, []string{chargefeed.ExpenseTopic, chargefeed.ProjectsTopic})
	if err != nil {
		return err
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	st := store.New(pool)
	a := api.New(st).WithReconciliation(st).WithRegistry(st)
	rec, err := buildReconciler(getenv, st)
	if err != nil {
		return err
	}
	if rec.ledger == nil {
		return errors.New("LEDGER_URL is required: a fed charge posts to the ledgers")
	}
	a.WithPosting(rec.poster, st, st)
	taxes, err := taxclient.FromEnv(getenv)
	if err != nil {
		return fmt.Errorf("tax client: %w", err)
	}
	if taxes != nil {
		a.WithTax(taxes)
	}
	client, err := kafka.New(kcfg)
	if err != nil {
		return err
	}
	defer client.Close()
	feed := &chargefeed.Feed{Submit: a, Entities: st, Config: cfg}
	c, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), eventing.Config{Name: chargefeed.ConsumerName, Idempotent: true,
		Handlers: feed.Handlers(), DeadLetterTopic: chargefeed.DeadLetterTopic, Log: slog.Default()})
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
	admin.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) { c.Metrics().Write(w, chargefeed.ConsumerName) })

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	go func() {
		err := c.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", chargefeed.ConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}
