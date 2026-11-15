package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target"
)

// Problem codes of the open orders load (#49).
const (
	CodeOrdersNotConfigured = "MIG-1570"
	CodeOrdersDenied        = "MIG-1571"
	CodeOrdersRefusedBatch  = "MIG-1572"
	CodeNoOrderLoad         = "MIG-1573"
)

// OrderTarget is the opening-order service the staged lines are loaded into; *target.Orders implements it for sales and purchasing.
type OrderTarget interface {
	Configured() bool
	LoadBatch(ctx context.Context, tenantID, legalEntityID, batchKey string, items []domain.OrderItem) (target.OpeningBatchResult, error)
	BatchTotals(ctx context.Context, tenantID, legalEntityID string, keep func(key string) bool) (domain.OrderTotals, error)
}

// WithSales sets the sales target of the SALES lines. Without one a run with sales lines answers 503 MIG-1570.
func (s *Server) WithSales(t OrderTarget) *Server { s.sales = t; return s }

// WithPurchasing sets the purchasing target of the PURCHASE lines. Without one a run with purchase lines answers 503 MIG-1570.
func (s *Server) WithPurchasing(t OrderTarget) *Server { s.purchasing = t; return s }

type orderLoadRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	// SiteID and WarehouseID are where sales ships from and purchasing receives to: the extract carries neither.
	SiteID      string `json:"siteId"`
	WarehouseID string `json:"warehouseId"`
	// Products, Customers and Vendors map a legacy item, customer or vendor to its Passage id; an id the party migration loaded is
	// found through the cross-reference, and one that is already a UUID needs no entry.
	Products  map[string]string `json:"products"`
	Customers map[string]string `json:"customers"`
	Vendors   map[string]string `json:"vendors"`
}

type orderLoadDTO struct {
	RunID         string     `json:"runId"`
	LegalEntityID string     `json:"legalEntityId"`
	Status        string     `json:"status"`
	Clean         bool       `json:"clean"`
	Staged        int        `json:"staged"`
	Loaded        int        `json:"loaded"`
	Rejected      int        `json:"rejected"`
	Pending       int        `json:"pending"`
	Attempts      int        `json:"attempts"`
	LastError     string     `json:"lastError,omitempty"`
	StartedAt     *time.Time `json:"startedAt,omitempty"`
	FinishedAt    *time.Time `json:"finishedAt,omitempty"`
	Version       int        `json:"version"`
}

func toOrderLoadDTO(l domain.OrderLoad) orderLoadDTO {
	return orderLoadDTO{RunID: l.RunID, LegalEntityID: l.LegalEntityID, Status: l.Status, Clean: l.Status == domain.LoadReconciled && l.Pending == 0 && l.Rejected == 0,
		Staged: l.Staged, Loaded: l.Loaded, Rejected: l.Rejected, Pending: l.Pending, Attempts: l.Attempts, LastError: l.LastError, StartedAt: l.StartedAt, FinishedAt: l.FinishedAt, Version: l.Version}
}

type orderRejectDTO struct {
	LegacyID  string `json:"legacyId"`
	OrderType string `json:"orderType"`
	Code      string `json:"code"`
	Detail    string `json:"detail"`
}

type orderLoadDetailDTO struct {
	orderLoadDTO
	Reconciliation []totalDTO       `json:"reconciliation"`
	Rejects        []orderRejectDTO `json:"rejects"`
}

func failOrderLoad(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNoOrderLoad):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoOrderLoad, "the run has not been loaded: post to orders-load first"))
	case errors.Is(err, store.ErrNotLoadable):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeOpeningNotLoadable, err.Error()))
	case errors.Is(err, store.ErrLoadBusy):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeLoadBusy, "another request is loading this run or another run of the same company; try again when it ends"))
	default:
		fail(w, r, err)
	}
}

// getOrderLoad serves GET /migration/runs/{id}/orders-load.
func (s *Server) getOrderLoad(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	l, err := s.store.GetOrderLoad(r.Context(), id)
	if err != nil {
		failOrderLoad(w, r, err)
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	rejects, err := s.store.ListOrderRejects(r.Context(), id, 50)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := orderLoadDetailDTO{orderLoadDTO: toOrderLoadDTO(l), Reconciliation: []totalDTO{}, Rejects: make([]orderRejectDTO, 0, len(rejects))}
	for _, t := range run.Totals {
		if strings.HasPrefix(t.Name, "target.open_orders.") {
			out.Reconciliation = append(out.Reconciliation, toTotalDTO(t))
		}
	}
	for _, v := range rejects {
		out.Rejects = append(out.Rejects, orderRejectDTO(v))
	}
	httpx.JSON(w, http.StatusOK, out)
}

func (s *Server) orderTarget(orderType string) OrderTarget {
	if orderType == domain.OrderPurchase {
		return s.purchasing
	}
	return s.sales
}

// resolveOrderMasters fills the maps of the request with the Passage ids the party and product migrations loaded, found through the
// cross-reference for every legacy item, customer and vendor of the pending lines that is not mapped and is not a UUID: the key is
// the legacy company and the code (company|code, as the migration service keys a record), else the code alone.
func (s *Server) resolveOrderMasters(ctx context.Context, tenantID, legalEntityID string, sys domain.SourceSystem, company string, pending []store.PendingOrderLine, req *orderLoadRequest) error {
	if s.xref == nil || !s.xref.Configured() {
		return nil
	}
	type kind struct {
		entity string
		have   map[string]string
		get    func(domain.OpenOrderLine) (string, bool)
	}
	kinds := []kind{
		{"item", req.Products, func(l domain.OpenOrderLine) (string, bool) { return l.Item, true }},
		{"customer", req.Customers, func(l domain.OpenOrderLine) (string, bool) { return l.Party, l.OrderType == domain.OrderSales }},
		{"vendor", req.Vendors, func(l domain.OpenOrderLine) (string, bool) { return l.Party, l.OrderType == domain.OrderPurchase }},
	}
	for _, k := range kinds {
		seen := map[string]bool{}
		var keys []string
		for _, p := range pending {
			code, applies := k.get(p.Line)
			code = strings.TrimSpace(code)
			if !applies || code == "" || k.have[code] != "" || ids.Valid(code) || seen[code] {
				continue
			}
			seen[code] = true
			keys = append(keys, company+"|"+code, code)
		}
		if len(keys) == 0 {
			continue
		}
		found, err := s.xref.Resolve(ctx, tenantID, legalEntityID, target.MigrationSystem(string(sys)), k.entity, keys)
		if err != nil {
			return httpx.NewProblem(http.StatusBadGateway, CodeXrefUnavailable, "the party migration's cross-reference could not be read to resolve the legacy "+k.entity+"s: "+err.Error())
		}
		for code := range seen {
			if id := found[company+"|"+code]; ids.Valid(id) {
				k.have[code] = id
			} else if id := found[code]; ids.Valid(id) {
				k.have[code] = id
			}
		}
	}
	return nil
}

// runOrderLoad serves POST /migration/runs/{id}/orders-load: send the staged lines of an open_orders run to sales (SALES lines) and
// purchasing (PURCHASE lines) as opening batches of at most 1000 lines, then read each target's batches back and reconcile their
// line count, open quantity and open value. A target takes a batch all or nothing, so a refusal that names lines marks those lines
// REJECTED and the rest of the chunk is sent again at once. The call can be repeated: lines a target already holds are not sent
// again, lines still pending after an outage are sent next time (the same lines under the same key are a replay, not a second batch).
func (s *Server) runOrderLoad(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req orderLoadRequest
	if err := httpx.DecodeLimit(r, &req, packRunBodyLimit); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	switch {
	case !ids.Valid(req.LegalEntityID):
		fail(w, r, invalid("legalEntityId must be a UUID"))
		return
	case !ids.Valid(req.SiteID) || !ids.Valid(req.WarehouseID):
		fail(w, r, invalid("siteId and warehouseId must be UUIDs: the extract carries no site or warehouse, so the load names the ones the opening orders ship from and are received into"))
		return
	case !tenant.InLegalEntityScope(p, req.LegalEntityID):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside your scope"))
		return
	}
	for name, m := range map[string]map[string]string{"products": req.Products, "customers": req.Customers, "vendors": req.Vendors} {
		for k, v := range m {
			if !ids.Valid(v) {
				fail(w, r, invalid(fmt.Sprintf("%s[%q] must be a UUID", name, k)))
				return
			}
		}
	}
	if req.Products == nil {
		req.Products = map[string]string{}
	}
	if req.Customers == nil {
		req.Customers = map[string]string{}
	}
	if req.Vendors == nil {
		req.Vendors = map[string]string{}
	}
	ctx := r.Context()
	load, run, pending, err := s.store.ClaimOrderLoad(ctx, id, req.LegalEntityID, p.UserID, loadLease)
	if err != nil {
		failOrderLoad(w, r, err)
		return
	}
	var needSales, needPurchasing bool
	for _, pl := range pending {
		needSales, needPurchasing = needSales || pl.Line.OrderType == domain.OrderSales, needPurchasing || pl.Line.OrderType == domain.OrderPurchase
	}
	if (needSales && (s.sales == nil || !s.sales.Configured())) || (needPurchasing && (s.purchasing == nil || !s.purchasing.Configured())) {
		s.abandonOrders(ctx, id, errors.New("a target is not configured"))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeOrdersNotConfigured,
			"the sales or purchasing target is not configured: set SALES_URL and PURCHASING_URL and the service signing key (PASSAGE_SERVICE_NAME=migration-factory, PASSAGE_SERVICE_SIGNING_KEY)"))
		return
	}
	if err := s.resolveOrderMasters(ctx, p.TenantID, req.LegalEntityID, domain.SourceSystem(run.System), run.Company, pending, &req); err != nil {
		s.abandonOrders(ctx, id, err)
		fail(w, r, err)
		return
	}
	maps := domain.OrderMaps{Products: req.Products, Customers: req.Customers, Vendors: req.Vendors, SiteID: req.SiteID, WarehouseID: req.WarehouseID}
	var unmapped []store.OrderReject
	chunks := map[string][]orderChunkLine{}
	for _, pl := range pending {
		item, code, why := domain.BuildOrderItem(pl.Line, maps)
		if code != "" {
			unmapped = append(unmapped, store.OrderReject{Line: pl, Code: code, Detail: why})
			continue
		}
		chunks[pl.Line.OrderType] = append(chunks[pl.Line.OrderType], orderChunkLine{pending: pl, item: item})
	}
	var fin store.OrderFinish
	fatalCode := CodeOrdersDenied
	if len(unmapped) > 0 {
		if err := s.store.RejectOrderLines(ctx, id, load.LegalEntityID, loadLease, unmapped); err != nil {
			s.abandonOrders(ctx, id, err)
			httpx.Fail(w, r, err)
			return
		}
	}
	for _, ot := range []string{domain.OrderSales, domain.OrderPurchase} {
		lines := chunks[ot]
		for start := 0; start < len(lines) && fin.Fatal == "" && fin.Unavailable == ""; start += domain.MaxOrderBatchLines {
			code, err := s.sendOrderChunk(ctx, p.TenantID, load, run, ot, lines[start:min(start+domain.MaxOrderBatchLines, len(lines))], &fin)
			if err != nil {
				s.abandonOrders(ctx, id, err)
				httpx.Fail(w, r, err)
				return
			}
			if code != "" {
				fatalCode = code
			}
		}
	}
	if fin.Fatal == "" && fin.Unavailable == "" {
		fin.Actual = map[string]domain.OrderTotals{}
		for _, ot := range []string{domain.OrderSales, domain.OrderPurchase} {
			tgt := s.orderTarget(ot)
			if tgt == nil || !tgt.Configured() {
				continue
			}
			targetName := domain.OrderTargetOf(ot)
			keep := func(key string) bool { return domain.IsOrderBatchKey(targetName, run.System, run.Company, key) }
			totals, err := tgt.BatchTotals(ctx, p.TenantID, load.LegalEntityID, keep)
			if err != nil {
				fin.ActualErr = "the " + targetName + " batches could not be read back: " + err.Error()
				break
			}
			fin.Actual[ot] = totals
		}
	}
	done, _, err := s.store.FinishOrderLoad(ctx, id, fin)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if done.Status == domain.LoadFailed {
		detail := "a target refused the factory's service token: "
		if fatalCode == CodeOrdersRefusedBatch {
			detail = "a target refused the batch request; its lines stay pending: "
		}
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, fatalCode, detail+done.LastError))
		return
	}
	httpx.JSON(w, http.StatusOK, toOrderLoadDTO(done))
}

type orderChunkLine struct {
	pending store.PendingOrderLine
	item    domain.OrderItem
}

func (s *Server) abandonOrders(ctx context.Context, runID string, cause error) {
	_, _, _ = s.store.FinishOrderLoad(context.WithoutCancel(ctx), runID, store.OrderFinish{Unavailable: cause.Error(), ActualErr: cause.Error()})
}

// takenByOwnBatch reports whether a refusal reason for a reference says another batch of the factory's own imported it: that
// batch was accepted by an earlier pass whose answer was lost, so the line is at the target already.
func takenByOwnBatch(detail string, own func(key string) bool) bool {
	const marker = "already imported by batch "
	i := strings.Index(detail, marker)
	return i >= 0 && own(strings.TrimSpace(detail[i+len(marker):]))
}

// sendOrderChunk sends one chunk of one order type, resending it without the lines the target refuses. It records its outcome in
// fin (Fatal or Unavailable stop the load) and returns the problem code to answer a fatal stop with. A non-nil error is unexpected
// and abandons the load.
func (s *Server) sendOrderChunk(ctx context.Context, tenantID string, load domain.OrderLoad, run store.OrderLoadRun, orderType string, chunk []orderChunkLine, fin *store.OrderFinish) (string, error) {
	targetName := domain.OrderTargetOf(orderType)
	tgt := s.orderTarget(orderType)
	own := func(key string) bool { return domain.IsOrderBatchKey(targetName, run.System, run.Company, key) }
	lines := chunk
	for round := 0; len(lines) > 0; round++ {
		if round >= maxRefusalRounds {
			fin.Fatal = fmt.Sprintf("the %s service kept refusing lines after %d attempts", targetName, maxRefusalRounds)
			return CodeOrdersRefusedBatch, nil
		}
		items := make([]domain.OrderItem, len(lines))
		hashes := make([]string, len(lines))
		for i, l := range lines {
			items[i], hashes[i] = l.item, l.item.Hash(targetName)
		}
		key := domain.OrderBatchKey(targetName, run.System, run.Company, hashes)
		res, err := tgt.LoadBatch(ctx, tenantID, load.LegalEntityID, key, items)
		var terr *target.Error
		var refused *target.RowsRefused
		switch {
		case err == nil:
			pend := make([]store.PendingOrderLine, len(lines))
			for i, l := range lines {
				pend[i] = l.pending
			}
			_ = res
			return "", s.store.RecordOrderBatch(ctx, load.RunID, load.LegalEntityID, loadLease, key, pend)
		case errors.As(err, &refused):
			var bad []store.OrderReject
			var held []store.PendingOrderLine
			drop := map[int]bool{}
			for row, why := range refused.Rows {
				l := lines[row-1].pending
				if takenByOwnBatch(why, own) {
					held = append(held, l)
				} else {
					bad = append(bad, store.OrderReject{Line: l, Code: refused.Code, Detail: why})
				}
				drop[row-1] = true
			}
			sort.Slice(bad, func(i, j int) bool { return bad[i].Line.LineID < bad[j].Line.LineID })
			if len(bad) > 0 {
				if err := s.store.RejectOrderLines(ctx, load.RunID, load.LegalEntityID, loadLease, bad); err != nil {
					return "", err
				}
			}
			if len(held) > 0 {
				if err := s.store.RecordOrderBatch(ctx, load.RunID, load.LegalEntityID, loadLease, "earlier-pass", held); err != nil {
					return "", err
				}
			}
			kept := make([]orderChunkLine, 0, len(lines)-len(drop))
			for i, l := range lines {
				if !drop[i] {
					kept = append(kept, l)
				}
			}
			lines = kept
		case errors.As(err, &terr) && (errors.Is(terr.Kind, target.ErrUnavailable) || errors.Is(terr.Kind, target.ErrBadAnswer)):
			fin.Unavailable = err.Error()
			return "", nil
		case errors.As(err, &terr) && errors.Is(terr.Kind, target.ErrDenied):
			fin.Fatal = err.Error()
			return CodeOrdersDenied, nil
		case errors.As(err, &terr) && errors.Is(terr.Kind, target.ErrRefused):
			fin.Fatal = err.Error()
			return CodeOrdersRefusedBatch, nil
		default:
			return "", err
		}
	}
	return "", nil
}
