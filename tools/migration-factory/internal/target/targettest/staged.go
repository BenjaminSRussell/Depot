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

// FakeStaged is a fake assets, production or payroll service for the opening import of the staged packs: it stores batches all or nothing under a
// key, refuses references another batch holds and lines named in Refuse, recomputes the control totals, lists its batches back with totals summed from
// the lines, and (assets and production) takes the request to book the opening journal.
type FakeStaged struct {
	*httptest.Server
	kind string // "assets", "production" or "payroll"

	mu      sync.Mutex
	batches map[string]*stagedBatch
	refs    map[string]string // reference -> batch key
	calls   []Call
	posts   []map[string]any
	// Refuse names line references the fake refuses as invalid lines.
	Refuse map[string]bool
	// Drift is added to the first total of every listed batch, to prove the reconciliation catches a target that holds something else.
	Drift decimal.Decimal
	// PostFailures makes the next n journal requests answer one failed document.
	PostFailures int
	// PostMore makes the next n journal requests say more is left.
	PostMore int
	failNext []int
	loseNext int
}

type stagedBatch struct {
	LegalEntityID, Key, Raw string
	Lines                   int
	Totals                  map[string]decimal.Decimal
	Extra                   map[string]any
}

// NewAssetsOpening starts the fake assets service; it closes with the test.
func NewAssetsOpening(t testing.TB) *FakeStaged { return newStaged(t, "assets") }

// NewProductionOpening starts the fake production service; it closes with the test.
func NewProductionOpening(t testing.TB) *FakeStaged { return newStaged(t, "production") }

// NewPayrollOpening starts the fake payroll service; it closes with the test.
func NewPayrollOpening(t testing.TB) *FakeStaged { return newStaged(t, "payroll") }

func newStaged(t testing.TB, kind string) *FakeStaged {
	f := &FakeStaged{kind: kind, batches: map[string]*stagedBatch{}, refs: map[string]string{}, Refuse: map[string]bool{}}
	path, postPath := "/v1/assets/opening-assets", "/v1/assets/gl-postings"
	switch kind {
	case "production":
		path, postPath = "/v1/production/opening-wip", "/v1/production/gl-postings:run"
	case "payroll":
		path, postPath = "/v1/payroll/opening-ytd", ""
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+path, f.post)
	mux.HandleFunc("GET "+path, f.list)
	if postPath != "" {
		mux.HandleFunc("POST "+postPath, f.journal)
	}
	f.Server = httptest.NewServer(mux)
	t.Cleanup(f.Close)
	return f
}

func (f *FakeStaged) duties() (put, get, post, invalid, taken string) {
	switch f.kind {
	case "production":
		return "production.opening-wip.import", "production.opening-wip.read", "production.journal.post", "PRD-OPB-001", "PRD-OPB-004"
	case "payroll":
		return "payroll.opening-ytd.import", "payroll.opening-ytd.read", "", "PAY-OPB-001", "PAY-OPB-004"
	}
	return "assets.opening-asset.import", "assets.opening-asset.read", "assets.journal.post", "AST-OPB-001", "AST-OPB-004"
}

// FailNext queues statuses the next requests answer, in order.
func (f *FakeStaged) FailNext(statuses ...int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failNext = append(f.failNext, statuses...)
}

// LoseNext makes the next n batch POSTs store the batch and then answer 503 (a timeout after the work was done).
func (f *FakeStaged) LoseNext(n int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.loseNext = n
}

// Calls returns the requests received so far.
func (f *FakeStaged) Calls() []Call {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Call(nil), f.calls...)
}

// Batches returns the stored batch keys in key order.
func (f *FakeStaged) Batches() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	keys := make([]string, 0, len(f.batches))
	for k := range f.batches {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// Extra returns the request fields (openingDate, journal) of a stored batch besides its lines.
func (f *FakeStaged) Extra(key string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	if b, ok := f.batches[key]; ok {
		return b.Extra
	}
	return nil
}

// JournalRequests returns the bodies of the journal requests received.
func (f *FakeStaged) JournalRequests() []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]map[string]any(nil), f.posts...)
}

func (f *FakeStaged) record(r *http.Request, status int) {
	c, ok := claims(r)
	call := Call{Method: r.Method, Path: r.URL.Path, IdempotencyKey: r.Header.Get("Idempotency-Key"), Status: status}
	if ok {
		call.Duties, call.Issuer, call.Act = c.Duties, c.Iss, c.Act
	}
	f.mu.Lock()
	f.calls = append(f.calls, call)
	f.mu.Unlock()
}

func (f *FakeStaged) admit(w http.ResponseWriter, r *http.Request, duty string) bool {
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

func (f *FakeStaged) refuse(w http.ResponseWriter, status int, code, detail string, fields map[string]string, rows ...map[string]any) {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"status": status, "code": code, "detail": detail, "title": http.StatusText(status), "errors": fields, "rowErrors": rows})
}

// measures reads the amounts a line adds to the totals, named measure.CURRENCY.
func (f *FakeStaged) measures(line map[string]any) map[string]decimal.Decimal {
	cur, _ := line["currency"].(string)
	str := func(k string) decimal.Decimal {
		v, _ := line[k].(string)
		d, _ := decimal.NewFromString(v)
		return d
	}
	switch f.kind {
	case "production":
		return map[string]decimal.Decimal{"material." + cur: str("material"), "labor." + cur: str("labor"), "overhead." + cur: str("overhead"), "wipValue." + cur: str("wipValue")}
	case "payroll":
		element, _ := line["element"].(string)
		return map[string]decimal.Decimal{strings.ToLower(element) + "." + cur: str("amount")}
	}
	return map[string]decimal.Decimal{"cost." + cur: str("acquisitionCost"), "accumulatedDepreciation." + cur: str("accumulatedDepreciation"), "nbv." + cur: str("netBookValue")}
}

func (f *FakeStaged) batchJSON(b *stagedBatch, drift decimal.Decimal) map[string]any {
	totals := map[string]string{}
	names := make([]string, 0, len(b.Totals))
	for n := range b.Totals {
		names = append(names, n)
	}
	sort.Strings(names)
	for i, n := range names {
		v := b.Totals[n]
		if i == 0 {
			v = v.Add(drift)
		}
		totals[n] = v.String()
	}
	return map[string]any{"id": b.Key, "legalEntityId": b.LegalEntityID, "batchKey": b.Key, "lineCount": b.Lines, "totals": totals}
}

func (f *FakeStaged) post(w http.ResponseWriter, r *http.Request) {
	put, _, _, invalid, taken := f.duties()
	if !f.admit(w, r, put) {
		return
	}
	var req map[string]json.RawMessage
	var raw json.RawMessage
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<20)).Decode(&raw); err != nil || json.Unmarshal(raw, &req) != nil {
		f.record(r, http.StatusBadRequest)
		problem(w, http.StatusBadRequest, "COM-0001", "malformed JSON body")
		return
	}
	var le, key string
	var lines []map[string]any
	var control map[string]string
	_ = json.Unmarshal(req["legalEntityId"], &le)
	_ = json.Unmarshal(req["batchKey"], &key)
	_ = json.Unmarshal(req["lines"], &lines)
	_ = json.Unmarshal(req["controlTotals"], &control)
	extra := map[string]any{}
	for k, v := range req {
		if k == "legalEntityId" || k == "batchKey" || k == "lines" || k == "controlTotals" {
			continue
		}
		var x any
		_ = json.Unmarshal(v, &x)
		extra[k] = x
	}
	if c, _ := claims(r); !strings.EqualFold(c.Le, le) {
		f.record(r, http.StatusForbidden)
		problem(w, http.StatusForbidden, "AUTH-0003", "legal entity is outside the token's scope")
		return
	}
	if key == "" || len(lines) == 0 || len(lines) > 1000 {
		f.record(r, http.StatusUnprocessableEntity)
		problem(w, http.StatusUnprocessableEntity, "COM-0001", "request failed validation")
		return
	}
	b := &stagedBatch{LegalEntityID: strings.ToLower(le), Key: key, Raw: string(raw), Lines: len(lines), Totals: map[string]decimal.Decimal{}, Extra: extra}
	f.mu.Lock()
	if held, ok := f.batches[key]; ok {
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
	var structured []map[string]any
	seen := map[string]bool{}
	for i, line := range lines {
		ref, _ := line["reference"].(string)
		field := "lines[" + strconv.Itoa(i+1) + "].reference"
		if ref == "" || seen[ref] {
			bad[field] = "is required and unique in the batch"
			continue
		}
		seen[ref] = true
		if other, ok := f.refs[ref]; ok {
			takenRows[field] = "already imported by batch " + other
			structured = append(structured, map[string]any{"row": i + 1, "field": "reference", "code": "REFERENCE_TAKEN", "takenByBatch": other})
			continue
		}
		if f.Refuse[ref] {
			bad[field] = "refused by the fake"
			continue
		}
		for k, v := range f.measures(line) {
			b.Totals[k] = b.Totals[k].Add(v)
		}
	}
	if len(bad) == 0 && len(takenRows) == 0 {
		for name, want := range control {
			if d, _ := decimal.NewFromString(want); !d.Equal(b.Totals[name]) {
				bad["controlTotals."+name] = "declared " + want + " but the lines add up to " + b.Totals[name].String()
			}
		}
	}
	if len(bad) > 0 || len(takenRows) > 0 {
		f.mu.Unlock()
		f.record(r, http.StatusUnprocessableEntity)
		if len(bad) > 0 {
			f.refuse(w, http.StatusUnprocessableEntity, invalid, "the batch has lines that cannot be imported; nothing was stored", bad)
		} else {
			f.refuse(w, http.StatusUnprocessableEntity, taken, "line references are already in use; nothing was stored", takenRows, structured...)
		}
		return
	}
	f.batches[key] = b
	for ref := range seen {
		f.refs[ref] = key
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

func (f *FakeStaged) list(w http.ResponseWriter, r *http.Request) {
	_, get, _, _, _ := f.duties()
	if !f.admit(w, r, get) {
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
		items = append(items, f.batchJSON(f.batches[k], f.Drift))
	}
	f.mu.Unlock()
	f.record(r, http.StatusOK)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "nextCursor": ""})
}

func (f *FakeStaged) journal(w http.ResponseWriter, r *http.Request) {
	_, _, post, _, _ := f.duties()
	if !f.admit(w, r, post) {
		return
	}
	var body map[string]any
	_ = json.NewDecoder(r.Body).Decode(&body)
	f.mu.Lock()
	f.posts = append(f.posts, body)
	failed := f.PostFailures > 0
	if failed {
		f.PostFailures--
	}
	more := f.PostMore > 0
	if more {
		f.PostMore--
	}
	f.mu.Unlock()
	f.record(r, http.StatusOK)
	out := map[string]any{"booked": []map[string]any{{"document": "opening", "entryId": "e1", "lines": 3}}, "failed": []map[string]any{}, "more": more}
	if failed {
		out["booked"], out["failed"] = []map[string]any{}, []map[string]any{{"document": "opening", "code": "LEDGER_PERIOD_CLOSED"}}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
