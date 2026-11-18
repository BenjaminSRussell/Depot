package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/sales/internal/store"
)

type openLinesStore struct {
	fakeStore
	filter store.OpenLineFilter
	calls  int
}

func (f *openLinesStore) ListOpenOrderLines(_ context.Context, filter store.OpenLineFilter) (store.OpenLinePage, error) {
	f.calls++
	f.filter = filter
	line := store.OpenOrderLine{LineID: ids.New(), OrderNumber: "SO-1", LineNo: 1, OpenQty: decimal.NewFromInt(120), DueDate: "2026-11-30"}
	return store.OpenLinePage{Items: []store.OpenOrderLine{line}, Next: "2026-11-30|" + line.LineID}, nil
}

func TestOpenOrderLines_RequireTenantAndTheOpenLinesDuty(t *testing.T) {
	st := &openLinesStore{}
	noTenant := principal(dutyOpenLines)
	noTenant.TenantID = ""
	if rec := serve(t, st, http.MethodGet, "/v1/sales/open-order-lines", noTenant, nil); rec.Code != http.StatusUnauthorized {
		t.Fatalf("no tenant: status %d body %s", rec.Code, rec.Body.String())
	}
	for _, duty := range []string{dutyRead, "sales.order.lookup", dutyWrite} {
		if rec := serve(t, st, http.MethodGet, "/v1/sales/open-order-lines", principal(duty), nil); rec.Code != http.StatusForbidden {
			t.Fatalf("duty %s: status %d, want 403", duty, rec.Code)
		}
	}
	if st.calls != 0 {
		t.Fatalf("a refused caller reached the store %d times", st.calls)
	}
}

func TestOpenOrderLines_RefuseMalformedQuery(t *testing.T) {
	st := &openLinesStore{}
	for _, query := range []string{"siteId=nope", "legalEntityId=nope", "limit=0", "limit=-1", "limit=abc", "limit=201", "dueTo=2026-13-40", "cursor=garbage", "cursor=2026-11-30%7Cnope"} {
		rec := serve(t, st, http.MethodGet, "/v1/sales/open-order-lines?"+query, principal(dutyOpenLines), nil)
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "COM-0001") {
			t.Errorf("%s: status %d body %s, want 400 COM-0001", query, rec.Code, rec.Body.String())
		}
	}
	if st.calls != 0 {
		t.Fatalf("a malformed query reached the store %d times", st.calls)
	}
}

func TestOpenOrderLines_RefuseALegalEntityOutsideTheCallersScope(t *testing.T) {
	st := &openLinesStore{}
	rec := serve(t, st, http.MethodGet, "/v1/sales/open-order-lines?legalEntityId="+ids.New(), principal(dutyOpenLines), nil)
	if rec.Code != http.StatusForbidden || st.calls != 0 {
		t.Fatalf("status %d calls %d body %s, want 403 and no store read", rec.Code, st.calls, rec.Body.String())
	}
}

func TestOpenOrderLines_PageCarriesTheFilterAndTheNextCursor(t *testing.T) {
	st := &openLinesStore{}
	site := ids.New()
	path := "/v1/sales/open-order-lines?legalEntityId=" + testLE + "&siteId=" + site + "&dueTo=2027-01-31&limit=2&cursor=2026-11-01%7C" + ids.New()
	rec := serve(t, st, http.MethodGet, path, principal(dutyOpenLines), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d body %s", rec.Code, rec.Body.String())
	}
	f := st.filter
	if f.LegalEntityID != testLE || f.SiteID != site || f.DueTo != "2027-01-31" || f.Limit != 2 || f.After == nil {
		t.Fatalf("filter %+v", f)
	}
	for _, want := range []string{`"openQty":"120"`, `"orderNumber":"SO-1"`, `"next":"2026-11-30|`} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body %s lacks %s", rec.Body.String(), want)
		}
	}
}
