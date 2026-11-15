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

	"github.com/BenjaminSRussell/passage/services/payables/internal/iccharge"
	"github.com/BenjaminSRussell/passage/services/payables/internal/store"
)

// icChargeSeeded reports whether the service principal table gives the consumer its principal with the one flag duty.
func icChargeSeeded(table map[string][]string) bool {
	return slices.Contains(table[iccharge.ConsumerName], iccharge.Duty)
}

// icAutoPost reads what posting an opted-in intercompany invoice needs: LEDGER_URL (and the service signing key) plus
// PAYABLES_IC_LEDGER_ID, PAYABLES_IC_EXPENSE_ACCOUNT, PAYABLES_IC_CONTROL_ACCOUNT, PAYABLES_IC_INPUT_TAX_ACCOUNT (needed when a charge
// carries tax) and PAYABLES_IC_PAYMENT_TERM_DAYS (default 30). Without them it returns nil: charges are still taken in for review, and a
// charge whose pairing opted in is refused after its intake row is stored.
func icAutoPost(getenv func(string) string) (*iccharge.AutoPost, error) {
	if getenv("PAYABLES_IC_LEDGER_ID") == "" && getenv("PAYABLES_IC_EXPENSE_ACCOUNT") == "" && getenv("PAYABLES_IC_CONTROL_ACCOUNT") == "" {
		return nil, nil
	}
	client, err := ledgerClient(getenv)
	if err != nil {
		return nil, err
	}
	auto := &iccharge.AutoPost{LedgerID: getenv("PAYABLES_IC_LEDGER_ID"), ExpenseAccount: getenv("PAYABLES_IC_EXPENSE_ACCOUNT"),
		InputTaxAccount: getenv("PAYABLES_IC_INPUT_TAX_ACCOUNT"), ControlAccount: getenv("PAYABLES_IC_CONTROL_ACCOUNT"), PaymentTermDays: 30}
	if client != nil {
		auto.Ledger = client
	}
	if raw := getenv("PAYABLES_IC_PAYMENT_TERM_DAYS"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 0 || n > 365 {
			return nil, errors.New("PAYABLES_IC_PAYMENT_TERM_DAYS must be 0 to 365")
		}
		auto.PaymentTermDays = n
	}
	if err := auto.Validate(); err != nil {
		return nil, err
	}
	return auto, nil
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
