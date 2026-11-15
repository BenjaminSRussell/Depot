package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"strconv"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/eventing/kafka"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/payables/internal/api"
	"github.com/BenjaminSRussell/passage/services/payables/internal/iccharge"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

// icChargeSeeded reports whether the service principal table gives the consumer its principal with the one flag duty.
func icChargeSeeded(table map[string][]string) bool {
	return slices.Contains(table[iccharge.ConsumerName], iccharge.Duty)
}

// icAutoPost builds what posting an opted-in intercompany invoice needs: the signed ledger client (LEDGER_URL and the service signing
// key) and the payables posting profiles (PAYABLES_POSTING_PROFILES), where each borrower legal entity names its ledger, its control
// account, and its intercompany cost and input tax accounts. It returns nil when none is configured: charges are then taken in for
// review, and one whose pairing opted in is left RECEIVED with the reason. LEDGER_URL without the signing key is an error.
func icAutoPost(getenv func(string) string) (*iccharge.AutoPost, error) {
	if getenv("LEDGER_URL") == "" {
		return nil, nil
	}
	client, err := ledgerClient(getenv)
	if err != nil {
		return nil, err
	}
	if client == nil || !client.Signed() {
		return nil, errors.New("auto-posting needs the service signing key with LEDGER_URL: the ledger refuses an unsigned journal")
	}
	profiles, err := api.LoadPostingProfiles(getenv)
	if err != nil {
		return nil, err
	}
	if profiles == nil {
		return nil, errors.New("auto-posting needs PAYABLES_POSTING_PROFILES: each legal entity's ledger and accounts")
	}
	auto := &iccharge.AutoPost{Ledger: client, PaymentTermDays: 30, Profiles: func(le string) (iccharge.EntityProfile, bool) {
		p, ok := profiles.For(le)
		return iccharge.EntityProfile{LedgerID: p.LedgerID, ExpenseAccount: p.ICChargeExpenseAccount, InputTaxAccount: p.ICChargeInputTaxAccount, ControlAccount: p.ControlAccount}, ok
	}}
	if raw := getenv("PAYABLES_IC_PAYMENT_TERM_DAYS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 365 {
			return nil, errors.New("PAYABLES_IC_PAYMENT_TERM_DAYS must be 0 to 365")
		}
		auto.PaymentTermDays = n
	}
	return auto, auto.Validate()
}

// consume runs the intercompany charge consumer (#40): it takes the borrower's vendor invoice of every chargeinvoice.requested into
// the vendor-invoice intake and serves only its admin port.
func consume(ctx context.Context, getenv func(string) string) error {
	if !icChargeSeeded(tenant.ServicePrincipalTable()) {
		return errors.New("ic charge consumer not started: service principal " + tenant.ServiceActor(iccharge.ConsumerName) + " is not seeded")
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	auto, err := icAutoPost(getenv)
	if err != nil {
		return err
	}
	kcfg, err := kafka.ConfigFromEnv(getenv, iccharge.ConsumerName, iccharge.ConsumerName, []string{iccharge.Topic})
	if err != nil {
		return err
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	client, err := kafka.New(kcfg)
	if err != nil {
		return err
	}
	defer client.Close()
	c, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), eventing.Config{Name: iccharge.ConsumerName, Idempotent: true,
		Handlers: iccharge.Handlers(store.New(pool), auto), DeadLetterTopic: iccharge.DeadLetterTopic, Log: slog.Default()})
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
	admin.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) { c.Metrics().Write(w, iccharge.ConsumerName) })

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	adminAddr := getenv("ADMIN_ADDR")
	if adminAddr == "" {
		adminAddr = ":9090"
	}
	go func() { errs <- httpx.Serve(ctx, adminAddr, admin) }()
	go func() {
		err := c.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", iccharge.ConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}
