package auditctx_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/auditctx"
	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
)

func TestFromDerivesTheActorFromTheVerifiedPrincipal(t *testing.T) {
	tid := testkit.TenantID(1)
	for _, tc := range []struct {
		name                    string
		p                       tenant.Principal
		actor, kind, onBehalfOf string
	}{
		{"person through the gateway", tenant.Principal{TenantID: tid, UserID: "ana"}, "ana", auditctx.ActorPerson, ""},
		{"service on its own", tenant.Principal{TenantID: tid, UserID: "service:receiving-ers", Service: "receiving"}, "service:receiving-ers", auditctx.ActorService, ""},
		{"in-process consumer", tenant.Principal{TenantID: tid, UserID: "service:pricing-cache"}, "service:pricing-cache", auditctx.ActorService, ""},
		{"service for a user", tenant.Principal{TenantID: tid, UserID: "ana", Service: "billing"}, "service:billing", auditctx.ActorService, "ana"},
		{"agent for a user", tenant.Principal{TenantID: tid, UserID: "ana", Service: "agents"}, "service:agents", auditctx.ActorAgent, "ana"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, err := auditctx.From(tenant.With(context.Background(), tc.p))
			if err != nil {
				t.Fatal(err)
			}
			if m.V != auditctx.Version || m.TenantID != tid || m.Actor != tc.actor || m.ActorKind != tc.kind || m.OnBehalfOf != tc.onBehalfOf || !ids.Valid(m.ChangeSetID) {
				t.Fatalf("message %+v", m)
			}
		})
	}
	if _, err := auditctx.From(context.Background()); err == nil {
		t.Fatal("no principal must be refused")
	}
	if _, err := auditctx.From(tenant.With(context.Background(), tenant.Principal{TenantID: tid})); err == nil {
		t.Fatal("no actor must be refused")
	}
}

func TestCaptureTakesTheReasonAndTheW3CTraceID(t *testing.T) {
	var got auditctx.Message
	h := middleware.RequestID(auditctx.Capture(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		ctx := tenant.With(r.Context(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "ana"})
		var err error
		if got, err = auditctx.From(ctx); err != nil {
			t.Fatal(err)
		}
	})))
	req := httptest.NewRequest(http.MethodPut, "/v1/party/vendors/v1/bank-accounts/b1", nil)
	req.Header.Set(auditctx.HeaderReason, "  vendor letter dated 3 Nov  ")
	req.Header.Set("traceparent", "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.Reason != "vendor letter dated 3 Nov" || got.TraceID != "4bf92f3577b34da6a3ce929d0e0e4736" {
		t.Fatalf("message %+v", got)
	}

	req = httptest.NewRequest(http.MethodPut, "/", nil)
	req.Header.Set("X-Request-Id", "req-7")
	req.Header.Set("traceparent", "00-00000000000000000000000000000000-00f067aa0ba902b7-01")
	req.Header.Set(auditctx.HeaderReason, strings.Repeat("é", auditctx.MaxReason+5))
	h.ServeHTTP(httptest.NewRecorder(), req)
	if got.TraceID != "req-7" || len([]rune(got.Reason)) != auditctx.MaxReason {
		t.Fatalf("an all-zero trace id falls back to the request id and the reason is cut: %q, %d", got.TraceID, len([]rune(got.Reason)))
	}
}

func TestWithCauseAndDecodeRoundTrip(t *testing.T) {
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "service:pricing-cache"})
	ctx = auditctx.WithTrace(auditctx.WithCause(auditctx.WithReason(ctx, "price list activated"), auditctx.Cause{EventType: "price.activated", EventID: "e-1", WorkflowInstanceID: "wf-1"}), "t-1")
	m, err := auditctx.From(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if m.EventID != "e-1" || m.EventType != "price.activated" || m.WorkflowInstanceID != "wf-1" || m.TraceID != "t-1" || m.Reason != "price list activated" {
		t.Fatalf("message %+v", m)
	}
	for _, body := range []string{`{`, `{"v":2,"tenantId":"` + testkit.TenantID(1) + `","actor":"a","actorKind":"person"}`,
		`{"v":1,"tenantId":"nope","actor":"a","actorKind":"person"}`, `{"v":1,"tenantId":"` + testkit.TenantID(1) + `","actorKind":"person"}`,
		`{"v":1,"tenantId":"` + testkit.TenantID(1) + `","actor":"a","actorKind":"robot"}`} {
		if _, err := auditctx.Decode([]byte(body)); err == nil {
			t.Errorf("Decode(%s) accepted", body)
		}
	}
}

// Emit writes a transactional logical-decoding message as depot_app, so a service needs no extra privilege. The
// test database runs with wal_level replica, where Postgres writes the record but nothing decodes it; the audit
// consumer tests feed the decoded form (services/audit/internal/cdc).
func TestWithAuditedTxEmitsAsTheAppRole(t *testing.T) {
	admin, app := testkit.DBWithApp(t)
	ctx := tenant.With(context.Background(), tenant.Principal{TenantID: testkit.TenantID(1), UserID: "ana"})
	if _, err := admin.Exec(ctx, `create table audited (tenant_id uuid not null, id int primary key)`); err != nil {
		t.Fatal(err)
	}
	if _, err := admin.Exec(ctx, dbx.RLSPolicy("audited")+` grant select, insert on audited to depot_app;
		do $$ begin execute format('grant usage on schema %I to depot_app', current_schema()); end $$;`); err != nil {
		t.Fatal(err)
	}
	if err := auditctx.WithAuditedTx(ctx, app, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into audited values ($1, 1)`, testkit.TenantID(1))
		return err
	}); err != nil {
		t.Fatal(err)
	}
	if err := auditctx.WithAuditedTx(context.Background(), app, func(pgx.Tx) error { return nil }); err == nil {
		t.Fatal("a transaction without a principal must be refused")
	}
}
