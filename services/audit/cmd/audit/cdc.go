package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"slices"
	"time"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/eventing/kafka"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/audit/internal/cdc"
	"github.com/BenjaminSRussell/depot/services/audit/internal/store"
)

const (
	cdcCommand   = "cdc-consume"
	chainCommand = "chain-nightly"
	// cdcDeadLetter is the default dead-letter topic; AUDIT_CDC_DLQ_TOPIC (chart consumer.kafka.dlqTopic) overrides it.
	cdcDeadLetter = "depot.audit.cdc.dlq"
	envDeadLetter = "AUDIT_CDC_DLQ_TOPIC"
	// anchorPrincipal is the nightly chain job's service principal; anchorDuty its flag duty.
	anchorPrincipal = "audit-anchor"
	anchorDuty      = "audit.chain.anchor"
	// contextRetention is how long depot.ctx messages are kept (cdc.ContextRetention).
	contextRetention = cdc.ContextRetention
)

// servicePrincipals is the table the start gates read; tests replace it.
var servicePrincipals = tenant.ServicePrincipalTable

func seeded(name, duty string) bool { return slices.Contains(servicePrincipals()[name], duty) }

// runCDCConsumer is `audit cdc-consume`: it reads the audit CDC topics (deploy/kafka/connect.yaml, audit-cdc-*
// connectors) and folds each captured row change into the change chain with the actor of its transaction's depot.ctx
// message (B-07). The inbox (consumer_inbox) makes it idempotent by CDC position. It serves /healthz, /readyz and
// /metrics on ADMIN_ADDR.
func runCDCConsumer(ctx context.Context, getenv func(string) string) error {
	if !seeded(cdc.ConsumerName, cdc.DutyApply) {
		return errors.New("CDC consumer not started: service principal " + tenant.ServiceActor(cdc.ConsumerName) + " with duty " + cdc.DutyApply + " is not seeded")
	}
	databaseURL := getenv("DATABASE_URL")
	if databaseURL == "" {
		return errors.New("DATABASE_URL is required")
	}
	registry := cdc.NewRegistry(cdc.Tables)
	cfg, err := kafka.ConfigFromEnv(getenv, cdc.ConsumerName, cdc.ConsumerName, registry.Topics())
	if err != nil {
		return err
	}
	// depot.ctx signatures are checked against the source services' ADR-0010 keys, the same registry the audit API
	// verifies service tokens with (DEPOT_SERVICE_NAME, DEPOT_SERVICE_PUBLIC_KEYS).
	keys, err := tenant.ServiceVerifierFromEnv(getenv)
	if err != nil {
		return fmt.Errorf("service public keys: %w", err)
	}
	handler := &cdc.Handler{Registry: registry, Log: slog.Default()}
	if keys != nil {
		handler.Verifier = keys
	} else {
		slog.Warn("DEPOT_SERVICE_PUBLIC_KEYS is unset: no depot.ctx message can be verified, so every captured change is recorded as unattributed-bad-signature or unattributed")
	}
	deadLetter := getenv(envDeadLetter)
	if deadLetter == "" {
		deadLetter = cdcDeadLetter
	}
	pool, err := dbx.Connect(ctx, databaseURL)
	if err != nil {
		return fmt.Errorf("connect database: %w", err)
	}
	defer pool.Close()
	client, err := kafka.New(cfg)
	if err != nil {
		return err
	}
	defer client.Close()
	handler.Sink = store.New(pool)
	consumer, err := eventing.NewConsumer(client, client, eventing.NewPgInbox(pool), eventing.Config{
		Name: cdc.ConsumerName, Handlers: handler.Handlers(), Decode: cdc.Decoder{Registry: registry}.Decode,
		DeadLetterTopic: deadLetter, Log: slog.Default(),
	})
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
		if err := pool.Ping(r.Context()); err != nil {
			slog.ErrorContext(r.Context(), "database not reachable", "err", err)
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	admin.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		consumer.Metrics().Write(w, cdc.ConsumerName)
		handler.WriteMetrics(w)
	})
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	errs := make(chan error, 2)
	go func() { errs <- httpx.Serve(ctx, addr(getenv, "ADMIN_ADDR", defaultAdminAddr), admin) }()
	go func() {
		err := consumer.Run(ctx)
		if err != nil && !errors.Is(err, context.Canceled) {
			slog.ErrorContext(ctx, "consumer stopped", "consumer", cdc.ConsumerName, "err", err)
		}
		errs <- err
	}()
	return <-errs
}

// anchorer is the store side of the nightly job.
type anchorer interface {
	AnchorDay(ctx context.Context, day time.Time) (store.Anchor, error)
	PurgeTxContexts(ctx context.Context, cutoff time.Time) (int64, error)
}

// chainNightly is `audit chain-nightly <tenant-id>...`, the CronJob: for each tenant (the app role cannot list them)
// it verifies the whole change chain, stores the day's anchor (the head seq and hash, once per tenant per UTC day) and
// purges depot.ctx messages older than seven days. Each anchor is also logged as one JSON line ("audit chain
// anchored"), a copy outside the database. It fails when any tenant's chain has a break, so the CronJob's failure
// alerts (runbook docs/ops/runbooks/audit-chain-verify-failure.md).
func chainNightly(ctx context.Context, getenv func(string) string, tenants []string) error {
	if len(tenants) == 0 {
		return errors.New("usage: audit " + chainCommand + " <tenant-id>...")
	}
	if !seeded(anchorPrincipal, anchorDuty) {
		return errors.New("chain job not started: service principal " + tenant.ServiceActor(anchorPrincipal) + " with duty " + anchorDuty + " is not seeded")
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
	return anchorTenants(ctx, store.New(pool), tenants, time.Now().UTC())
}

func anchorTenants(ctx context.Context, s anchorer, tenants []string, now time.Time) error {
	var broken []string
	for _, tid := range tenants {
		p, err := tenant.ServicePrincipal(anchorPrincipal, tid, "")
		if err != nil {
			return fmt.Errorf("tenant %q: %w", tid, err)
		}
		tctx := tenant.With(ctx, p)
		a, err := s.AnchorDay(tctx, now)
		if err != nil {
			return fmt.Errorf("tenant %s: anchor: %w", tid, err)
		}
		purged, err := s.PurgeTxContexts(tctx, now.Add(-contextRetention))
		if err != nil {
			return fmt.Errorf("tenant %s: purge depot.ctx messages: %w", tid, err)
		}
		c := a.Chain
		slog.InfoContext(ctx, "audit chain anchored", "tenantId", p.TenantID, "date", a.Date, "headSeq", a.HeadSeq, "headHash", a.HeadHash,
			"created", a.Created, "entries", c.Entries, "verified", c.Verified, "breaks", c.Breaks,
			"legacyPrecision", c.KnownBreaks.LegacyPrecision, "legacyFork", c.KnownBreaks.LegacyFork,
			"anchorsChecked", c.Anchors.Checked, "contextsPurged", purged)
		if !c.Verified {
			slog.ErrorContext(ctx, "audit change chain verification failed", "tenantId", p.TenantID, "breaks", c.Breaks, "firstBreak", c.FirstBreak)
			broken = append(broken, p.TenantID)
		}
	}
	if len(broken) > 0 {
		return fmt.Errorf("change chain broken for tenants %v", broken)
	}
	return nil
}
