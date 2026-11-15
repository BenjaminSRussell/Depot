package targettest

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/shopspring/decimal"
)

// FakeOrders is a fake sales or purchasing service for the opening-order import: it stores batches all or nothing under a key,
// refuses references another batch holds and lines named in Refuse, and lists its batches back with their totals.
type FakeOrders struct {
	*httptest.Server
	kind string // "sales" or "purchasing"

	mu      sync.Mutex
	batches map[string]*orderBatch
	refs    map[string]string // reference -> batch key
	calls   []Call
	// Refuse names line references the fake refuses as invalid lines.
	Refuse map[string]bool
	// ValueDrift is added to the open value of every listed batch, to prove the reconciliation catches a target that holds something else.
	ValueDrift decimal.Decimal
	failNext   []int
	loseNext   int
}

type orderBatch struct {
	LegalEntityID, Key, Raw string
	Lines                   int
	Qty                     decimal.Decimal
	Value                   map[string]decimal.Decimal
}

// NewSalesOrders starts the fake sales service; it closes with the test.
func NewSalesOrders(t testing.TB) *FakeOrders { return newOrders(t, "sales") }

// NewPurchasingOrders starts the fake purchasing service; it closes with the test.
func NewPurchasingOrders(t testing.TB) *FakeOrders { return newOrders(t, "purchasing") }

func newOrders(t testing.TB, kind string) *FakeOrders {
	f := &FakeOrders{kind: kind, batches: map[string]*orderBatch{}, refs: map[string]string{}, Refuse: map[string]bool{}}
	path := "/v1/sales/opening-orders"
	if kind == "purchasing" {
		path = "/v1/procurement/opening-orders"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+path, f.post)
	mux.HandleFunc("GET "+path, f.list)
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

// FailNext queues statuses the next requests answer, in order.
func (f *FakeOrders) FailNext(statuses ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = append(f.failNext, statuses...)
}

// LoseNext makes the next n batch POSTs store the batch and then answer 503 (a timeout after the work was done).
func (f *FakeOrders) LoseNext(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loseNext = n
}

// Calls returns the requests received so far.
func (f *FakeOrders) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Batches returns the stored batch keys in key order.
func (f *FakeOrders) Batches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.batches))
	for k := range f.batches {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (f *FakeOrders) record(r *http.Request, status int) {
	c, ok := claims(r)
	call := Call{Method: r.Method, Path: r.URL.Path, IdempotencyKey: r.Header.Get("Idempotency-Key"), Status: status}
	if ok {
		call.Duties, call.Issuer, call.Act = c.Duties, c.Iss, c.Act
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}

// admit checks the token and the injected failures; it answers the request itself when it refuses.
func (f *FakeOrders) admit(w http.ResponseWriter, r *http.Request, duty string) bool {
	c, ok := claims(r)
	f.mu.Lock()
	if len(f.failNext) > 0 {
		status := f.failNext[0]
		f.failNext = f.failNext[1:]
		f.mu.Unlock()
		f.record(r, status)
		problem(w, status, "COM-0500", "injected failure")
		return false
	}
	f.mu.Unlock()
	held := false
	for _, d := range c.Duties {
		held = held || d == duty
	}
	switch {
	case !ok:
		f.record(r, http.StatusUnauthorized)
		problem(w, http.StatusUnauthorized, "AUTH-0001", "no service token")
		return false
	case !held || c.Aud != f.kind:
		f.record(r, http.StatusForbidden)
		problem(w, http.StatusForbidden, "AUTH-0002", "duty "+duty+" required")
		return false
	}
	return true
}

func (f *FakeOrders) refuse(w http.ResponseWriter, status int, code, detail string, fields map[string]string) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "code": code, "detail": detail, "title": http.StatusText(status), "errors": fields})
}

func (f *FakeOrders) batchJSON(b *orderBatch, drift decimal.Decimal) map[string]any {
	value := map[string]string{}
	for c, v := range b.Value {
		value[c] = v.Add(drift).String()
	}
	return map[string]any{"id": b.Key, "legalEntityId": b.LegalEntityID, "batchKey": b.Key, "orderCount": 1, "lineCount": b.Lines, "openQty": b.Qty.String(), "openValue": value}
}

func (f *FakeOrders) post(w http.ResponseWriter, r *http.Request) {
	duty, invalid := "sales.opening-order.import", "SLS-OPB-001"
	taken := "SLS-OPB-004"
	if f.kind == "purchasing" {
		duty, invalid, taken = "procurement.opening-order.import", "PRC-OPB-001", "PRC-OPB-004"
	}
	if !f.admit(w, r, duty) {
		return
	}
	var raw json.RawMessage
	var req struct {
		LegalEntityID string            `json:"legalEntityId"`
		BatchKey      string            `json:"batchKey"`
		ControlTotals map[string]string `json:"controlTotals"`
		Lines         []json.RawMessage `json:"lines"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&raw); err != nil || json.Unmarshal(raw, &req) != nil {
		f.record(r, http.StatusBadRequest)
		problem(w, http.StatusBadRequest, "COM-0001", "malformed JSON body")
		return
	}
	if c, _ := claims(r); !strings.EqualFold(c.Le, req.LegalEntityID) {
		f.record(r, http.StatusForbidden)
		problem(w, http.StatusForbidden, "AUTH-0003", "legal entity is outside the token's scope")
		return
	}
	if req.BatchKey == "" || len(req.Lines) == 0 || len(req.Lines) > 1000 {
		f.record(r, http.StatusUnprocessableEntity)
		problem(w, http.StatusUnprocessableEntity, "COM-0001", "request failed validation")
		return
	}
	b := &orderBatch{LegalEntityID: strings.ToLower(req.LegalEntityID), Key: req.BatchKey, Raw: string(raw), Lines: len(req.Lines), Value: map[string]decimal.Decimal{}}
	f.mu.Lock()
	if held, ok := f.batches[req.BatchKey]; ok {
		f.mu.Unlock()
		if held.Raw != b.Raw {
			f.record(r, http.StatusConflict)
			f.refuse(w, http.StatusConflict, strings.Replace(invalid, "001", "002", 1), "batch key was already imported with different content", nil)
			return
		}
		f.record(r, http.StatusOK)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(f.batchJSON(held, decimal.Zero))
		return
	}
	bad, takenRows := map[string]string{}, map[string]string{}
	seen := map[string]bool{}
	for i, rawLine := range req.Lines {
		var l struct {
			Reference    string `json:"reference"`
			OrderedQty   string `json:"orderedQty"`
			DeliveredQty string `json:"deliveredQty"`
			ReceivedQty  string `json:"receivedQty"`
			UnitPrice    string `json:"unitPrice"`
			Currency     string `json:"currency"`
		}
		field := "lines[" + strconv.Itoa(i+1) + "].reference"
		if json.Unmarshal(rawLine, &l) != nil || l.Reference == "" || seen[l.Reference] {
			bad[field] = "is required and unique in the batch"
			continue
		}
		seen[l.Reference] = true
		if other, ok := f.refs[l.Reference]; ok {
			takenRows[field] = "already imported by batch " + other
			continue
		}
		if f.Refuse[l.Reference] {
			bad[field] = "refused by the fake"
			continue
		}
		ordered, e1 := decimal.NewFromString(l.OrderedQty)
		done, e2 := decimal.NewFromString(l.DeliveredQty + l.ReceivedQty)
		price, e3 := decimal.NewFromString(l.UnitPrice)
		if e1 != nil || e2 != nil || e3 != nil || !ordered.Sub(done).IsPositive() {
			bad["lines["+strconv.Itoa(i+1)+"].orderedQty"] = "must leave an open quantity"
			continue
		}
		open := ordered.Sub(done)
		b.Qty = b.Qty.Add(open)
		b.Value[l.Currency] = b.Value[l.Currency].Add(open.Mul(price).Round(6))
	}
	if len(bad) > 0 || len(takenRows) > 0 {
		f.mu.Unlock()
		f.record(r, http.StatusUnprocessableEntity)
		if len(bad) > 0 {
			f.refuse(w, http.StatusUnprocessableEntity, invalid, "the batch has lines that cannot be imported; nothing was stored", bad)
		} else {
			f.refuse(w, http.StatusUnprocessableEntity, taken, "line references are already in use; nothing was stored", takenRows)
		}
		return
	}
	f.batches[req.BatchKey] = b
	for ref := range seen {
		f.refs[ref] = req.BatchKey
	}
	lose := f.loseNext > 0
	if lose {
		f.loseNext--
	}
	f.mu.Unlock()
	if lose {
		f.record(r, http.StatusServiceUnavailable)
		problem(w, http.StatusServiceUnavailable, "COM-0500", "the batch was stored and the answer was lost")
		return
	}
	f.record(r, http.StatusCreated)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusCreated)
	_ = json.NewEncoder(w).Encode(f.batchJSON(b, decimal.Zero))
}

func (f *FakeOrders) list(w http.ResponseWriter, r *http.Request) {
	duty := "sales.opening-order.import.read"
	if f.kind == "purchasing" {
		duty = "procurement.opening-order.import.read"
	}
	if !f.admit(w, r, duty) {
		return
	}
	le := strings.ToLower(r.URL.Query().Get("legal_entity"))
	f.mu.Lock()
	var keys []string
	for k, b := range f.batches {
		if b.LegalEntityID == le {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	items := make([]map[string]any, 0, len(keys))
	for _, k := range keys {
		items = append(items, f.batchJSON(f.batches[k], f.ValueDrift))
	}
	f.mu.Unlock()
	f.record(r, http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "nextCursor": ""})
}
