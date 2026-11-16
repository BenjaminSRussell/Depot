package api

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/store"
	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/target"
)

// Problem codes of the staged packs' load (#49).
const (
	CodeStagedNotConfigured = "MIG-1580"
	CodeStagedDenied        = "MIG-1581"
	CodeStagedRefusedBatch  = "MIG-1582"
	CodeNoStagedLoad        = "MIG-1583"
)

// StagedTarget is the opening-balance service the staged lines are loaded into; *target.Staged implements it for assets, production and payroll.
type StagedTarget interface {
	Configured() bool
	LoadBatch(ctx context.Context, tenantID, legalEntityID, batchKey string, extra map[string]any, lines []domain.WireLine) (target.OpeningBatchResult, error)
	BatchTotals(ctx context.Context, tenantID, legalEntityID string, keep func(key string) bool) (domain.StagedTotals, error)
	CanPostJournal() bool
	PostJournal(ctx context.Context, tenantID, legalEntityID string, body map[string]any) (target.JournalResult, error)
}

// WithAssets sets the target of a fixed_assets run. Without one the load answers 503 MIG-1580.
func (s *Server) WithAssets(t StagedTarget) *Server { s.assets = t; return s }

// WithProduction sets the target of a wip_prod run. Without one the load answers 503 MIG-1580.
func (s *Server) WithProduction(t StagedTarget) *Server { s.production = t; return s }

// WithPayroll sets the target of a payroll_ytd run. Without one the load answers 503 MIG-1580.
func (s *Server) WithPayroll(t StagedTarget) *Server { s.payroll = t; return s }

func (s *Server) stagedTarget(pack domain.Pack) StagedTarget {
	switch pack {
	case domain.PackFixedAssets:
		return s.assets
	case domain.PackWIPProd:
		return s.production
	case domain.PackPayrollYTD:
		return s.payroll
	}
	return nil
}

type stagedLoadRequest struct {
	LegalEntityID string `json:"legalEntityId"`
	// OpeningDate is the cutover date the opening transactions and the opening journal carry (fixed_assets, wip_prod).
	OpeningDate string `json:"openingDate"`
	// LedgerID is the ledger the opening journal is booked in (fixed_assets with journal post, wip_prod).
	LedgerID string `json:"ledgerId"`
	// Journal is post (book the opening journal through the general ledger) or linked (the ledger already holds the balances); fixed_assets and
	// wip_prod. payroll_ytd holds balances only.
	Journal string `json:"journal"`
	// DepreciatedThrough is the last month the legacy system depreciated, 2026-12 (fixed_assets).
	DepreciatedThrough string `json:"depreciatedThrough"`
	// AssetClass is the class of an asset whose line names none (fixed_assets).
	AssetClass string `json:"assetClass"`
	// SiteID and WarehouseID are where the opening production orders run: the extract carries neither (wip_prod).
	SiteID      string `json:"siteId"`
	WarehouseID string `json:"warehouseId"`
	// Products and Workers map a legacy item or worker to its Passage id; an id the migration loaded is found through the cross-reference, and one that
	// is already a UUID needs no entry.
	Products map[string]string `json:"products"`
	Workers  map[string]string `json:"workers"`
}

type stagedLoadDTO struct {
	RunID         string     `json:"runId"`
	Pack          string     `json:"pack"`
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

func toStagedLoadDTO(l domain.StagedLoad) stagedLoadDTO {
	return stagedLoadDTO{RunID: l.RunID, Pack: string(l.Pack), LegalEntityID: l.LegalEntityID, Status: l.Status, Clean: l.Status == domain.LoadReconciled && l.Pending == 0 && l.Rejected == 0,
		Staged: l.Staged, Loaded: l.Loaded, Rejected: l.Rejected, Pending: l.Pending, Attempts: l.Attempts, LastError: l.LastError, StartedAt: l.StartedAt, FinishedAt: l.FinishedAt, Version: l.Version}
}

type stagedRejectDTO struct {
	LegacyID string `json:"legacyId"`
	Code     string `json:"code"`
	Detail   string `json:"detail"`
}

type stagedLoadDetailDTO struct {
	stagedLoadDTO
	Reconciliation []totalDTO        `json:"reconciliation"`
	Rejects        []stagedRejectDTO `json:"rejects"`
}

func failStagedLoad(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, store.ErrNoStagedLoad):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeNoStagedLoad, "the run has not been loaded: post to staged-load first"))
	case errors.Is(err, store.ErrNotLoadable):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeOpeningNotLoadable, err.Error()))
	case errors.Is(err, store.ErrLoadBusy):
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeLoadBusy, "another request is loading this run or another run of the same company; try again when it ends"))
	default:
		fail(w, r, err)
	}
}

// getStagedLoad serves GET /migration/runs/{id}/staged-load.
func (s *Server) getStagedLoad(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	l, err := s.store.GetStagedLoad(r.Context(), id)
	if err != nil {
		failStagedLoad(w, r, err)
		return
	}
	run, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	rejects, err := s.store.ListStagedRejects(r.Context(), id, 50)
	if err != nil {
		fail(w, r, err)
		return
	}
	out := stagedLoadDetailDTO{stagedLoadDTO: toStagedLoadDTO(l), Reconciliation: []totalDTO{}, Rejects: make([]stagedRejectDTO, 0, len(rejects))}
	for _, t := range run.Totals {
		if strings.HasPrefix(t.Name, "target."+string(l.Pack)+".") {
			out.Reconciliation = append(out.Reconciliation, toTotalDTO(t))
		}
	}
	for _, v := range rejects {
		out.Rejects = append(out.Rejects, stagedRejectDTO(v))
	}
	httpx.JSON(w, http.StatusOK, out)
}

var monthDay = regexp.MustCompile(`^[0-9]{4}-[0-9]{2}-[0-9]{2}$`)

// checkStagedRequest validates what the pack's load needs of the request.
func checkStagedRequest(pack domain.Pack, req *stagedLoadRequest) error {
	if !ids.Valid(req.LegalEntityID) {
		return invalid("legalEntityId must be a UUID")
	}
	for name, m := range map[string]map[string]string{"products": req.Products, "workers": req.Workers} {
		for k, v := range m {
			if !ids.Valid(v) {
				return invalid(fmt.Sprintf("%s[%q] must be a UUID", name, k))
			}
		}
	}
	if req.Products == nil {
		req.Products = map[string]string{}
	}
	if req.Workers == nil {
		req.Workers = map[string]string{}
	}
	validDate := func() bool {
		if !monthDay.MatchString(req.OpeningDate) {
			return false
		}
		_, err := time.Parse("2006-01-02", req.OpeningDate)
		return err == nil
	}
	switch pack {
	case domain.PackFixedAssets:
		if _, _, err := domain.ParseDepreciatedThrough(req.DepreciatedThrough); err != nil {
			return invalid(err.Error() + ": the last month the legacy system depreciated, which places each book's schedule")
		}
		switch {
		case req.Journal != "post" && req.Journal != "linked":
			return invalid("journal must be post (book the opening journal through the general ledger) or linked (the ledger already holds the asset accounts' balances)")
		case !validDate():
			return invalid("openingDate must be the cutover date, such as 2027-01-01")
		case req.Journal == "post" && !ids.Valid(req.LedgerID):
			return invalid("ledgerId must be the ledger UUID the opening journal is booked in")
		}
	case domain.PackWIPProd:
		switch {
		case !ids.Valid(req.SiteID) || !ids.Valid(req.WarehouseID):
			return invalid("siteId and warehouseId must be UUIDs: the extract carries no site or warehouse, so the load names the ones the opening production orders run in")
		case !validDate():
			return invalid("openingDate must be the cutover date, such as 2027-01-01")
		case req.Journal != "post" && req.Journal != "linked":
			return invalid("journal must be post (book the opening WIP journal through the general ledger) or linked (the ledger already holds the WIP balance)")
		case req.Journal == "post" && !ids.Valid(req.LedgerID):
			return invalid("ledgerId must be the ledger UUID the opening WIP journal is booked in")
		}
	}
	return nil
}

type stagedChunkLine struct {
	pending store.PendingStagedLine
	wire    []domain.WireLine
}

// stagedBatches builds the pending lines and cuts them into batches of at most 1000 target lines; a line that cannot be built is returned as a reject.
func stagedBatches(pack domain.Pack, pending []store.PendingStagedLine, maps domain.StagedMaps) ([][]stagedChunkLine, []store.StagedReject) {
	var batches [][]stagedChunkLine
	var cur []stagedChunkLine
	n := 0
	var rejects []store.StagedReject
	for _, p := range pending {
		wire, code, why := domain.BuildStagedLines(pack, p.Line, maps)
		if code != "" {
			rejects = append(rejects, store.StagedReject{Line: p, Code: code, Detail: why})
			continue
		}
		if n+len(wire) > stagedBatchLimit && n > 0 {
			batches = append(batches, cur)
			cur, n = nil, 0
		}
		cur = append(cur, stagedChunkLine{pending: p, wire: wire})
		n += len(wire)
	}
	if len(cur) > 0 {
		batches = append(batches, cur)
	}
	return batches, rejects
}

// stagedBatchLimit is the most target lines one batch holds; a variable only so a test can make it small.
var stagedBatchLimit = domain.MaxStagedBatchLines

// resolveStagedMasters fills the maps of the request with the Passage ids the migration loaded, found through the cross-reference for every legacy
// item (wip_prod) or worker (payroll_ytd) of the pending lines that is not mapped and is not a UUID: the key is the legacy company and the code
// (company|code, as the migration service keys a record), else the code alone.
func (s *Server) resolveStagedMasters(ctx context.Context, tenantID, legalEntityID string, sys domain.SourceSystem, company string, pack domain.Pack, pending []store.PendingStagedLine, req *stagedLoadRequest) error {
	if s.xref == nil || !s.xref.Configured() {
		return nil
	}
	entity, have, field := "", map[string]string(nil), ""
	switch pack {
	case domain.PackWIPProd:
		entity, have, field = "item", req.Products, "item"
	case domain.PackPayrollYTD:
		entity, have, field = "worker", req.Workers, "worker"
	default:
		return nil
	}
	seen := map[string]bool{}
	var keys []string
	for _, p := range pending {
		code := strings.TrimSpace(p.Line.Group[field])
		if code == "" || have[code] != "" || ids.Valid(code) || seen[code] {
			continue
		}
		seen[code] = true
		keys = append(keys, company+"|"+code, code)
	}
	if len(keys) == 0 {
		return nil
	}
	found, err := s.xref.Resolve(ctx, tenantID, legalEntityID, target.MigrationSystem(string(sys)), entity, keys)
	if err != nil {
		return httpx.NewProblem(http.StatusBadGateway, CodeXrefUnavailable, "the migration's cross-reference could not be read to resolve the legacy "+entity+"s: "+err.Error())
	}
	for code := range seen {
		if id := found[company+"|"+code]; ids.Valid(id) {
			have[code] = id
		} else if id := found[code]; ids.Valid(id) {
			have[code] = id
		}
	}
	return nil
}

// runStagedLoad serves POST /migration/runs/{id}/staged-load: send the staged lines of a fixed_assets, wip_prod or payroll_ytd run to assets, production
// or payroll as opening batches of at most 1000 lines, read the target's batches back and reconcile their line count and totals, and, for assets (journal
// post) and production, have the service book the opening journal. A target takes a batch all or nothing, so a refusal that names lines marks those
// lines REJECTED and the rest of the batch is sent again at once. The call can be repeated: lines a target already holds are not sent again, lines still
// pending after an outage are sent next time (the same lines under the same key are a replay, not a second batch).
func (s *Server) runStagedLoad(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		fail(w, r, err)
		return
	}
	p, ok := principal(w, r)
	if !ok {
		return
	}
	var req stagedLoadRequest
	if err := httpx.DecodeLimit(r, &req, packRunBodyLimit); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	run0, err := s.store.GetRun(r.Context(), id)
	if err != nil {
		fail(w, r, err)
		return
	}
	pack := run0.Pack
	if domain.StagedTargetOfPack(pack) == "" {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, CodeOpeningNotLoadable, "pack "+string(pack)+" has no staged load; only fixed_assets, wip_prod and payroll_ytd runs are loaded into assets, production and payroll"))
		return
	}
	if err := checkStagedRequest(pack, &req); err != nil {
		fail(w, r, err)
		return
	}
	if !tenant.InLegalEntityScope(p, req.LegalEntityID) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusForbidden, "AUTH-0003", "the legal entity is outside your scope"))
		return
	}
	ctx := r.Context()
	tgt := s.stagedTarget(pack)
	load, run, pending, err := s.store.ClaimStagedLoad(ctx, id, req.LegalEntityID, p.UserID, loadLease)
	if err != nil {
		failStagedLoad(w, r, err)
		return
	}
	if tgt == nil || !tgt.Configured() {
		s.abandonStaged(ctx, id, errors.New("the target is not configured"))
		httpx.Fail(w, r, httpx.NewProblem(http.StatusServiceUnavailable, CodeStagedNotConfigured,
			"the "+domain.StagedTargetOfPack(pack)+" target is not configured: set ASSETS_URL, PRODUCTION_URL or PAYROLL_URL and the service signing key (PASSAGE_SERVICE_NAME=migration-factory, PASSAGE_SERVICE_SIGNING_KEY)"))
		return
	}
	if err := s.resolveStagedMasters(ctx, p.TenantID, req.LegalEntityID, domain.SourceSystem(run.System), run.Company, pack, pending, &req); err != nil {
		s.abandonStaged(ctx, id, err)
		fail(w, r, err)
		return
	}
	maps := domain.StagedMaps{Products: req.Products, Workers: req.Workers, SiteID: req.SiteID, WarehouseID: req.WarehouseID, AssetClass: req.AssetClass, DepreciatedThrough: req.DepreciatedThrough}
	batches, unmapped := stagedBatches(pack, pending, maps)
	var fin store.StagedFinish
	fatalCode := CodeStagedDenied
	if len(unmapped) > 0 {
		if err := s.store.RejectStagedLines(ctx, id, load.LegalEntityID, loadLease, unmapped); err != nil {
			s.abandonStaged(ctx, id, err)
			httpx.Fail(w, r, err)
			return
		}
	}
	extra := map[string]any{}
	if pack == domain.PackFixedAssets {
		extra["openingDate"], extra["journal"] = req.OpeningDate, req.Journal
	}
	if pack == domain.PackWIPProd {
		extra["openingDate"], extra["journal"] = req.OpeningDate, req.Journal
	}
	for _, batch := range batches {
		if fin.Fatal != "" || fin.Unavailable != "" {
			break
		}
		code, err := s.sendStagedBatch(ctx, p.TenantID, load, run, tgt, extra, batch, &fin)
		if err != nil {
			s.abandonStaged(ctx, id, err)
			httpx.Fail(w, r, err)
			return
		}
		if code != "" {
			fatalCode = code
		}
	}
	if fin.Fatal == "" && fin.Unavailable == "" {
		if problem := s.bookStagedJournal(ctx, p.TenantID, id, pack, req, tgt, &fin); problem != "" {
			fin.ActualErr = problem
		}
	}
	if fin.Fatal == "" && fin.Unavailable == "" && fin.ActualErr == "" {
		keep := func(key string) bool { return domain.IsStagedBatchKey(pack, run.System, run.Company, key) }
		totals, err := tgt.BatchTotals(ctx, p.TenantID, load.LegalEntityID, keep)
		if err != nil {
			fin.ActualErr = "the " + domain.StagedTargetOfPack(pack) + " batches could not be read back: " + err.Error()
		} else {
			fin.Actual = totals
		}
	}
	done, _, err := s.store.FinishStagedLoad(ctx, id, fin)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if done.Status == domain.LoadFailed {
		detail := "the target refused the factory's service token: "
		if fatalCode == CodeStagedRefusedBatch {
			detail = "the target refused the batch request; its lines stay pending: "
		}
		httpx.Fail(w, r, httpx.NewProblem(http.StatusBadGateway, fatalCode, detail+done.LastError))
		return
	}
	httpx.JSON(w, http.StatusOK, toStagedLoadDTO(done))
}

// bookStagedJournal has the target book the opening journal of the run: for assets (journal post) one call per book and currency until the target has
// nothing left, for production one call for the legal entity. It returns a problem text, or "" when nothing is to book or all is booked.
func (s *Server) bookStagedJournal(ctx context.Context, tenantID, runID string, pack domain.Pack, req stagedLoadRequest, tgt StagedTarget, fin *store.StagedFinish) string {
	if !tgt.CanPostJournal() || req.Journal != "post" {
		return ""
	}
	var bodies []map[string]any
	switch pack {
	case domain.PackFixedAssets:
		books, err := s.store.ListStagedBooks(ctx, runID)
		if err != nil {
			return "the books to book could not be listed: " + err.Error()
		}
		for _, b := range books {
			bodies = append(bodies, map[string]any{"legalEntityId": req.LegalEntityID, "ledgerId": req.LedgerID, "bookCode": b.Book, "currency": b.Currency})
		}
	case domain.PackWIPProd:
		bodies = append(bodies, map[string]any{"legalEntityId": req.LegalEntityID, "ledgerId": req.LedgerID})
	}
	for _, body := range bodies {
		for round := 0; ; round++ {
			if round >= maxJournalRounds {
				return fmt.Sprintf("the opening journal is still not fully booked after %d calls; load again to continue", maxJournalRounds)
			}
			res, err := tgt.PostJournal(ctx, tenantID, req.LegalEntityID, body)
			var terr *target.Error
			switch {
			case errors.As(err, &terr) && (errors.Is(terr.Kind, target.ErrUnavailable) || errors.Is(terr.Kind, target.ErrBadAnswer)):
				fin.Unavailable = err.Error()
				return ""
			case err != nil:
				return "the opening journal could not be booked: " + err.Error()
			case len(res.Failed) > 0:
				return "the ledger refused part of the opening journal (" + strings.Join(res.Failed, ", ") + "); load again once the cause is fixed"
			case !res.More:
				goto next
			}
		}
	next:
	}
	return ""
}

// maxJournalRounds bounds the calls that book one journal group (200 documents each).
const maxJournalRounds = 200

func (s *Server) abandonStaged(ctx context.Context, runID string, cause error) {
	_, _, _ = s.store.FinishStagedLoad(context.WithoutCancel(ctx), runID, store.StagedFinish{Unavailable: cause.Error(), ActualErr: cause.Error()})
}

// sendStagedBatch sends one batch, resending it without the lines a target refuses. It records its outcome in fin (Fatal or Unavailable stop the load)
// and returns the problem code to answer a fatal stop with. A non-nil error is unexpected and abandons the load.
func (s *Server) sendStagedBatch(ctx context.Context, tenantID string, load domain.StagedLoad, run store.StagedLoadRun, tgt StagedTarget, extra map[string]any, lines []stagedChunkLine, fin *store.StagedFinish) (string, error) {
	targetName := domain.StagedTargetOfPack(run.Pack)
	own := func(key string) bool { return domain.IsStagedBatchKey(run.Pack, run.System, run.Company, key) }
	for round := 0; len(lines) > 0; round++ {
		if round >= maxRefusalRounds {
			fin.Fatal = fmt.Sprintf("the %s service kept refusing lines after %d attempts", targetName, maxRefusalRounds)
			return CodeStagedRefusedBatch, nil
		}
		var wire []domain.WireLine
		var owner []int
		for li, l := range lines {
			for _, w := range l.wire {
				wire = append(wire, w)
				owner = append(owner, li)
			}
		}
		hashes := make([]string, len(wire))
		for i, w := range wire {
			hashes[i] = w.Hash(targetName)
		}
		key := domain.StagedBatchKey(run.Pack, run.System, run.Company, hashes)
		_, err := tgt.LoadBatch(ctx, tenantID, load.LegalEntityID, key, extra, wire)
		var terr *target.Error
		var refused *target.RowsRefused
		switch {
		case err == nil:
			pend := make([]store.PendingStagedLine, len(lines))
			for i, l := range lines {
				pend[i] = l.pending
			}
			return "", s.store.RecordStagedBatch(ctx, load.RunID, load.LegalEntityID, loadLease, key, pend)
		case errors.As(err, &refused):
			// Per staged line: loaded by an earlier pass (a reference is held by a batch of the factory's own), or refused.
			loadedBefore := map[int]bool{}
			reason := map[int]string{}
			for row, why := range refused.Rows {
				li := owner[row-1]
				if ownBatchTakes(refused.Errors, row, own) {
					loadedBefore[li] = true
					continue
				}
				if _, seen := reason[li]; !seen {
					reason[li] = why
				}
			}
			var rejects []store.StagedReject
			var done []store.PendingStagedLine
			var kept []stagedChunkLine
			for li, l := range lines {
				switch why, bad := reason[li]; {
				case bad:
					rejects = append(rejects, store.StagedReject{Line: l.pending, Code: refused.Code, Detail: why})
				case loadedBefore[li]:
					done = append(done, l.pending)
				default:
					kept = append(kept, l)
				}
			}
			if len(rejects) == 0 && len(done) == 0 {
				fin.Fatal = err.Error()
				return CodeStagedRefusedBatch, nil
			}
			if len(rejects) > 0 {
				if err := s.store.RejectStagedLines(ctx, load.RunID, load.LegalEntityID, loadLease, rejects); err != nil {
					return "", err
				}
			}
			if len(done) > 0 {
				if err := s.store.RecordStagedBatch(ctx, load.RunID, load.LegalEntityID, loadLease, "earlier-pass", done); err != nil {
					return "", err
				}
			}
			lines = kept
		case errors.As(err, &terr) && (errors.Is(terr.Kind, target.ErrUnavailable) || errors.Is(terr.Kind, target.ErrBadAnswer)):
			fin.Unavailable = err.Error()
			return "", nil
		case errors.As(err, &terr) && errors.Is(terr.Kind, target.ErrDenied):
			fin.Fatal = err.Error()
			return CodeStagedDenied, nil
		case errors.As(err, &terr) && errors.Is(terr.Kind, target.ErrRefused):
			fin.Fatal = err.Error()
			return CodeStagedRefusedBatch, nil
		default:
			return "", err
		}
	}
	return "", nil
}
