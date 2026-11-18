package api

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/productblock"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/sales/internal/domain"
	"github.com/BenjaminSRussell/depot/services/sales/internal/store"
)

// fakeStore records what the handlers ask of the store. Methods a test does
// not set panic through the nil embedded interface.
type fakeStore struct {
	store.Store
	order        store.Order
	released     bool
	filter       store.OrderFilter
	lineFilter   store.OrderLineFilter
	knownLine    string
	profileLE    string
	profile      store.CreditProfile
	listedLE     *string
	releaseCalls int
	rejectCalls  int
	rejectBody   string
}

func (f *fakeStore) GetOrder(context.Context, string) (store.Order, error) { return f.order, nil }

func (f *fakeStore) ReleaseHold(_ context.Context, _, _ string, _ int, _ string, _ time.Time) (store.ConfirmResult, error) {
	f.releaseCalls++
	return store.ConfirmResult{Order: f.order, Confirmed: true}, nil
}

func (f *fakeStore) RejectHold(_ context.Context, _, _ string, _ int, comment, _ string, _ time.Time) (store.Order, error) {
	f.rejectCalls++
	f.rejectBody = comment
	o := f.order
	o.Status = "CANCELLED"
	o.Version++
	return o, nil
}

func (f *fakeStore) ListOrders(_ context.Context, filter store.OrderFilter) (store.OrderPage, error) {
	f.filter = filter
	return store.OrderPage{Items: []store.OrderSummary{}}, nil
}

func (f *fakeStore) ListOrderLines(_ context.Context, filter store.OrderLineFilter) ([]store.OrderLineSummary, error) {
	f.lineFilter = filter
	if filter.ID != "" && f.knownLine == filter.ID {
		return []store.OrderLineSummary{{ID: filter.ID, OrderNumber: "SO-1", LineNo: 1}}, nil
	}
	return []store.OrderLineSummary{}, nil
}

func (f *fakeStore) GetCreditProfile(_ context.Context, le, _ string) (store.CreditProfile, error) {
	f.profileLE = le
	return f.profile, nil
}

func (f *fakeStore) ListCreditProfiles(_ context.Context, le string, _ int) ([]store.CreditProfile, error) {
	f.listedLE = &le
	return []store.CreditProfile{f.profile}, nil
}

func serve(t *testing.T, s store.Store, method, path string, p tenant.Principal, headers map[string]string) *httptest.ResponseRecorder {
	t.Helper()
	a := New(s)
	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(tenant.With(req.Context(), p)))
		})
	})
	a.Register(r)
	req := httptest.NewRequest(method, path, nil)
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

const testLE = "00000000-0000-0000-0000-0000000000b1"

func principal(duties ...string) tenant.Principal {
	p := tenant.Principal{TenantID: testkit.TenantID(1), UserID: "u1", LegalEntityID: testLE, Duties: map[string]struct{}{}}
	for _, d := range duties {
		p.Duties[d] = struct{}{}
	}
	return p
}

func allScopePrincipal(duties ...string) tenant.Principal {
	p := principal(duties...)
	p.LegalEntityID = ""
	p.Duties[tenant.DutyAllScope] = struct{}{}
	return p
}

// TestReleaseHold_DutyPerHoldType checks PRD §5.27.17: CREDIT holds need
// CreditRelease and the other hold types need SalesOrderHold, even though the
// route admits either duty.
func TestReleaseHold_DutyPerHoldType(t *testing.T) {
	orderID, holdID := ids.New(), ids.New()
	path := orders + "/" + orderID + "/holds/" + holdID + ":release"
	tests := []struct {
		holdType string
		duty     string
		want     int
	}{
		{domain.HoldCredit, dutyCreditRelease, http.StatusOK},
		{domain.HoldCredit, dutyHold, http.StatusForbidden},
		{domain.HoldFraud, dutyHold, http.StatusOK},
		{domain.HoldFraud, dutyCreditRelease, http.StatusForbidden},
		{domain.HoldManual, dutyCreditRelease, http.StatusForbidden},
	}
	for _, tc := range tests {
		t.Run(tc.holdType+" with "+tc.duty, func(t *testing.T) {
			f := &fakeStore{order: store.Order{ID: orderID, Version: 2, Holds: []store.Hold{{ID: holdID, HoldType: tc.holdType, Status: domain.HoldActive}}}}
			rec := serve(t, f, http.MethodPost, path, principal(tc.duty), map[string]string{"If-Match": `"2"`})
			if rec.Code != tc.want {
				t.Fatalf("status %d want %d: %s", rec.Code, tc.want, rec.Body.String())
			}
			if (tc.want == http.StatusOK) != (f.releaseCalls == 1) {
				t.Fatalf("store release calls %d", f.releaseCalls)
			}
			if tc.want == http.StatusOK && !strings.Contains(rec.Body.String(), `"confirmed":true`) {
				t.Fatalf("release response must carry confirmed: %s", rec.Body.String())
			}
		})
	}
}

func TestReleaseHold_RequiresIfMatch(t *testing.T) {
	f := &fakeStore{}
	rec := serve(t, f, http.MethodPost, orders+"/"+ids.New()+"/holds/"+ids.New()+":release", principal(dutyCreditRelease), nil)
	if rec.Code != http.StatusPreconditionRequired || f.releaseCalls != 0 {
		t.Fatalf("status %d calls %d", rec.Code, f.releaseCalls)
	}
}

// TestRejectHold covers the route's duty (CreditRelease only), the required
// comment, If-Match and the cancelled order in the response.
func TestRejectHold(t *testing.T) {
	orderID, holdID := ids.New(), ids.New()
	path := orders + "/" + orderID + "/holds/" + holdID + ":reject"
	match := map[string]string{"If-Match": `"2"`}
	post := func(f *fakeStore, duty, body string, headers map[string]string) *httptest.ResponseRecorder {
		return serveBody(t, f, http.MethodPost, path, body, principal(duty), headers)
	}

	f := &fakeStore{order: store.Order{ID: orderID, Version: 2}}
	rec := post(f, dutyCreditRelease, `{"comment":"  over the limit "}`, match)
	if rec.Code != http.StatusOK || f.rejectCalls != 1 || f.rejectBody != "over the limit" {
		t.Fatalf("status %d calls %d comment %q: %s", rec.Code, f.rejectCalls, f.rejectBody, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), `"status":"CANCELLED"`) || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("response %s etag %s", rec.Body.String(), rec.Header().Get("ETag"))
	}

	f = &fakeStore{}
	if rec := post(f, dutyHold, `{"comment":"x"}`, match); rec.Code != http.StatusForbidden || f.rejectCalls != 0 {
		t.Fatalf("sales.order.hold must not reject: %d", rec.Code)
	}
	for name, body := range map[string]string{"missing": `{}`, "blank": `{"comment":"   "}`, "long": `{"comment":"` + strings.Repeat("x", 501) + `"}`} {
		f = &fakeStore{}
		if rec := post(f, dutyCreditRelease, body, match); rec.Code != http.StatusBadRequest || f.rejectCalls != 0 {
			t.Fatalf("%s comment: status %d", name, rec.Code)
		}
	}
	f = &fakeStore{}
	if rec := post(f, dutyCreditRelease, `{"comment":"x"}`, nil); rec.Code != http.StatusPreconditionRequired || f.rejectCalls != 0 {
		t.Fatalf("no If-Match: %d", rec.Code)
	}
	bad := orders + "/" + orderID + "/holds/not-a-uuid:reject"
	if rec := serveBody(t, &fakeStore{}, http.MethodPost, bad, `{"comment":"x"}`, principal(dutyCreditRelease), match); rec.Code != http.StatusNotFound {
		t.Fatalf("bad hold id: %d", rec.Code)
	}
}

func TestListOrders_ValidatesAndPassesFilters(t *testing.T) {
	customer := ids.New()
	f := &fakeStore{}
	rec := serve(t, f, http.MethodGet, orders+"?status=DRAFT&hold=CREDIT&customerId="+customer+"&limit=10&q=+so-12+", principal(dutyRead), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	if f.filter != (store.OrderFilter{Status: "DRAFT", HoldType: "CREDIT", CustomerID: customer, Limit: 10, Q: "so-12"}) {
		t.Fatalf("filter %+v", f.filter)
	}
	if !strings.Contains(rec.Body.String(), `"items":[]`) {
		t.Fatalf("empty list must be an empty array: %s", rec.Body.String())
	}
	for _, q := range []string{"status=SHIPPING", "hold=LEGAL", "customerId=x", "limit=0", "limit=201", "cursor=abc", "q=" + strings.Repeat("x", 101)} {
		if rec := serve(t, &fakeStore{}, http.MethodGet, orders+"?"+q, principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s: status %d", q, rec.Code)
		}
	}
	if rec := serve(t, &fakeStore{}, http.MethodGet, orders, principal(dutyWrite), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("list without the read duty: %d", rec.Code)
	}
}

func TestCreditProfile_LegalEntityAndWireDates(t *testing.T) {
	le, customer := testLE, ids.New()
	review := time.Date(2027, 6, 30, 0, 0, 0, 0, time.UTC)
	f := &fakeStore{profile: store.CreditProfile{LegalEntityID: le, CustomerID: customer, Version: 3,
		CreditProfile: domain.CreditProfile{Limit: decimal.NewFromInt(10000), Currency: "USD", LimitType: domain.LimitBalance, ReviewDate: &review}}}
	rec := serve(t, f, http.MethodGet, creditProfiles+"/"+customer+"?legalEntityId="+le, principal(dutyRead), nil)
	if rec.Code != http.StatusOK || f.profileLE != le {
		t.Fatalf("status %d le %q: %s", rec.Code, f.profileLE, rec.Body.String())
	}
	if body := rec.Body.String(); !strings.Contains(body, `"reviewDate":"2027-06-30"`) || !strings.Contains(body, `"limit":"10000"`) || rec.Header().Get("ETag") != `"3"` {
		t.Fatalf("wire form %s etag %s", body, rec.Header().Get("ETag"))
	}
	if rec := serve(t, f, http.MethodGet, creditProfiles+"/"+customer, allScopePrincipal(dutyRead), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("no legal entity: %d", rec.Code)
	}
	scoped := principal(dutyRead)
	scoped.LegalEntityID = ids.New()
	if rec := serve(t, f, http.MethodGet, creditProfiles+"/"+customer+"?legalEntityId="+le, scoped, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("another legal entity than the caller's scope: %d", rec.Code)
	}
	if rec := serve(t, f, http.MethodGet, creditProfiles, allScopePrincipal(dutyRead), nil); rec.Code != http.StatusOK || f.listedLE == nil || *f.listedLE != "" {
		t.Fatalf("list across the caller's scope: %d %v", rec.Code, f.listedLE)
	}
}

func TestLegalEntityScope_RefusesCallerWithoutScope(t *testing.T) {
	noScope := principal(dutyRead, dutyWrite)
	noScope.LegalEntityID = ""
	le, customer := ids.New(), ids.New()
	if rec := serve(t, &fakeStore{}, http.MethodGet, creditProfiles+"/"+customer+"?legalEntityId="+le, noScope, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("credit profile get: %d", rec.Code)
	}
	if rec := serve(t, &fakeStore{}, http.MethodGet, creditProfiles, noScope, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("credit profile list: %d", rec.Code)
	}
	if rec := serveBody(t, &fakeStore{}, http.MethodPost, orders, orderBody(le), noScope, map[string]string{idempotencyKey: "k-noscope"}); rec.Code != http.StatusForbidden {
		t.Fatalf("create order: %d %s", rec.Code, rec.Body.String())
	}
}

func TestListOrderLines_PassesFiltersAndAcceptsTheLookupDuty(t *testing.T) {
	fs := &fakeStore{}
	order := ids.New()
	for _, duty := range []string{dutyRead, "sales.order.lookup"} {
		rec := serve(t, fs, http.MethodGet, "/v1/sales/order-lines?q=+so-1+&limit=7&orderId="+order, principal(duty), nil)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s: status = %d body %s", duty, rec.Code, rec.Body.String())
		}
	}
	if fs.lineFilter.Q != "so-1" || fs.lineFilter.Limit != 7 || fs.lineFilter.OrderID != order {
		t.Fatalf("filter = %+v", fs.lineFilter)
	}
	if rec := serve(t, fs, http.MethodGet, "/v1/sales/order-lines?orderId=x", principal(dutyRead), nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("a bad orderId: status = %d", rec.Code)
	}
	if rec := serve(t, fs, http.MethodGet, "/v1/sales/order-lines", principal("sales.other"), nil); rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
}

func TestGetOrderLine_ByIdOr404(t *testing.T) {
	line := ids.New()
	fs := &fakeStore{knownLine: line}
	if rec := serve(t, fs, http.MethodGet, "/v1/sales/order-lines/"+line, principal("sales.order.lookup"), nil); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "SO-1") {
		t.Fatalf("known line: %d %s", rec.Code, rec.Body)
	}
	for _, path := range []string{"/v1/sales/order-lines/" + ids.New(), "/v1/sales/order-lines/nope"} {
		if rec := serve(t, fs, http.MethodGet, path, principal(dutyRead), nil); rec.Code != http.StatusNotFound {
			t.Fatalf("%s: %d", path, rec.Code)
		}
	}
}

const blockedForSales = "This product is blocked for sales. Ask a product data steward to lift the block."

func withBlocks(c productblock.Checker) func(*API) *API {
	return func(a *API) *API { return a.WithProductBlocks(c) }
}

func requireBlockedForSales(t *testing.T, rec *httptest.ResponseRecorder) {
	t.Helper()
	var p httpx.Problem
	if err := json.Unmarshal(rec.Body.Bytes(), &p); err != nil {
		t.Fatalf("body %q is not a problem: %v", rec.Body.String(), err)
	}
	if rec.Code != http.StatusUnprocessableEntity || p.Code != productblock.CodeBlocked || p.Title != "Unprocessable Entity" || p.Detail != blockedForSales {
		t.Fatalf("got %d %+v, want 422 %s with a plain title and the detail naming sales", rec.Code, p, productblock.CodeBlocked)
	}
}

func TestCreateQuotation_RefusesAProductBlockedForSales(t *testing.T) {
	req := quoteRequest(1)
	req.LegalEntityID = testLE
	blocked := req.Lines[0].ProductID
	raw, _ := json.Marshal(req)
	post := func(s *slice4Store, opts ...func(*API) *API) *httptest.ResponseRecorder {
		return serveBody(t, s, http.MethodPost, quotations, string(raw), principal(dutyQuotation), map[string]string{idempotencyKey: "q1"}, opts...)
	}

	s := &slice4Store{}
	fake := &productblock.Fake{BlockedDetail: map[string]string{blocked: blockedForSales}}
	requireBlockedForSales(t, post(s, withBlocks(fake)))
	if s.quotation != nil {
		t.Fatal("a refused quotation reached the store")
	}
	if got := fake.Calls[0]; got != blocked+"|"+productblock.SalesQuotation {
		t.Fatalf("checked %q, want the quotation process", got)
	}
	if rec := post(s, withBlocks(&productblock.Fake{})); rec.Code != http.StatusCreated {
		t.Fatalf("unblocked product: %d %s", rec.Code, rec.Body)
	}
	s = &slice4Store{}
	if rec := post(s); rec.Code != http.StatusCreated {
		t.Fatalf("no checker configured must enforce nothing: %d %s", rec.Code, rec.Body)
	}
	down := &productblock.Fake{Err: httpx.NewProblem(http.StatusServiceUnavailable, productblock.CodeUnavailable, "try again")}
	s = &slice4Store{}
	if rec := post(s, withBlocks(down)); rec.Code != http.StatusServiceUnavailable || s.quotation != nil {
		t.Fatalf("checker unavailable fails closed: %d %s", rec.Code, rec.Body)
	}
}

func TestCreateOrder_RefusesAProductBlockedForSales(t *testing.T) {
	req := validOrderRequest()
	req.LegalEntityID = testLE
	blocked := req.Lines[0].ProductID
	raw, _ := json.Marshal(req)
	post := func(s *portalWriteStore, opts ...func(*API) *API) *httptest.ResponseRecorder {
		return serveBody(t, s, http.MethodPost, orders, string(raw), principal(dutyWrite), map[string]string{idempotencyKey: "o1"}, opts...)
	}

	s := newPortalWriteStore()
	fake := &productblock.Fake{BlockedDetail: map[string]string{blocked: blockedForSales}}
	requireBlockedForSales(t, post(s, withBlocks(fake)))
	if len(s.createdOrder) != 0 {
		t.Fatal("a refused order reached the store")
	}
	if got := fake.Calls[0]; got != blocked+"|"+productblock.SalesOrder {
		t.Fatalf("checked %q, want the sales order process", got)
	}
	if rec := post(s, withBlocks(&productblock.Fake{})); rec.Code != http.StatusCreated {
		t.Fatalf("unblocked product: %d %s", rec.Code, rec.Body)
	}
	if rec := post(newPortalWriteStore()); rec.Code != http.StatusCreated {
		t.Fatalf("no checker configured must enforce nothing: %d %s", rec.Code, rec.Body)
	}
}

func TestCustomerPortal_PlaceOrderRefusesAProductBlockedForSales(t *testing.T) {
	s := newPortalWriteStore()
	w := newWriteHarness(t, s, nil, withBlocks(&productblock.Fake{BlockedDetail: map[string]string{prodPump: blockedForSales}}))
	requireBlockedForSales(t, w.post(t, custA, customerPortal+"/orders", "submit-0001", portalOrderBody(prodPump, "3")))
	if len(s.createdOrder) != 0 {
		t.Fatal("a refused portal order reached the store")
	}
}

func TestConvertQuotation_RefusesAProductBlockedForSalesAfterTheQuote(t *testing.T) {
	id := ids.New()
	blocked, other := ids.New(), ids.New()
	quote := store.Quotation{ID: id, LegalEntityID: testLE, CustomerID: ids.New(), Lines: []store.QuotationLine{
		{Line: store.Line{LineNo: 1, ProductID: blocked}}, {Line: store.Line{LineNo: 2, ProductID: other}}}}
	s := &convertStore{quote: quote}
	opt := withBlocks(&productblock.Fake{BlockedDetail: map[string]string{blocked: blockedForSales}})
	convert := func(body string) *httptest.ResponseRecorder {
		return serveBody(t, s, http.MethodPost, quotations+"/"+id+":convert", body, principal(dutyQuotation), map[string]string{"If-Match": `"1"`}, opt)
	}

	requireBlockedForSales(t, convert(""))
	requireBlockedForSales(t, convert(`{"lineNos":[1]}`))
	if s.convert != nil {
		t.Fatal("a refused conversion reached the store")
	}
	if rec := convert(`{"lineNos":[2]}`); rec.Code != http.StatusCreated {
		t.Fatalf("converting only the unblocked line: %d %s", rec.Code, rec.Body)
	}
}

func TestPatchOrder_RefusesAnAddedLineBlockedForSales(t *testing.T) {
	blocked, free := ids.New(), ids.New()
	body := func(product string) string {
		return `{"reason":"add","addLines":[{"productId":"` + product + `","quantity":"3","unit":"EA","siteId":"` + ids.New() + `","warehouseId":"` + ids.New() +
			`","unitPrice":"2","requestedShipDate":"2027-03-04"}]}`
	}
	c := &changeStore{}
	c.order = store.Order{ID: ids.New(), LegalEntityID: testLE}
	fake := &productblock.Fake{BlockedDetail: map[string]string{blocked: blockedForSales}}
	patch := func(product string, opts ...func(*API) *API) *httptest.ResponseRecorder {
		return serveBody(t, c, http.MethodPatch, orders+"/"+c.order.ID, body(product), principal(dutyWrite), map[string]string{"If-Match": `"3"`}, opts...)
	}

	requireBlockedForSales(t, patch(blocked, withBlocks(fake)))
	if c.change != nil {
		t.Fatal("a refused change reached the store")
	}
	if got := fake.Calls[0]; got != blocked+"|"+productblock.SalesOrder {
		t.Fatalf("checked %q, want the sales order process", got)
	}
	if rec := patch(free, withBlocks(&productblock.Fake{})); rec.Code != http.StatusOK || c.change == nil {
		t.Fatalf("unblocked product: %d %s", rec.Code, rec.Body)
	}
}

type convertStore struct {
	slice4Store
	quote store.Quotation
}

func (c *convertStore) GetQuotation(context.Context, string) (store.Quotation, error) {
	return c.quote, nil
}
