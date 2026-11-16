package target

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/tools/migration-factory/internal/domain"
)

// The assets, production and payroll clients (#49) send the staged packs fixed_assets, wip_prod and payroll_ytd to POST /v1/assets/opening-assets,
// POST /v1/production/opening-wip and POST /v1/payroll/opening-ytd, read the stored batches back from GET and, for assets and production, ask the
// service to book the opening journal. All sign as the factory like the other clients do (own audience, one duty per token).

const (
	audAssets     = "assets"
	audProduction = "production"
	audPayroll    = "payroll"

	dutyAssetsImport     = "assets.opening-asset.import"
	dutyAssetsImportRead = "assets.opening-asset.read"
	dutyAssetsPost       = "assets.opening-journal.post"
	dutyProductionImport = "production.opening-wip.import"
	dutyProductionRead   = "production.opening-wip.read"
	dutyProductionPost   = "production.opening-journal.post"
	dutyPayrollImport    = "payroll.opening-ytd.import"
	dutyPayrollRead      = "payroll.opening-ytd.read"

	pathAssetsOpening     = "/v1/assets/opening-assets"
	pathAssetsPost        = "/v1/assets/opening-journal"
	pathProductionOpening = "/v1/production/opening-wip"
	pathProductionPost    = "/v1/production/opening-journal"
	pathPayrollOpening    = "/v1/payroll/opening-ytd"

	// Problem codes of the three targets that name rows.
	CodeAssetsRowsInvalid     = "AST-OPB-001"
	CodeAssetsRowsTaken       = "AST-OPB-004"
	CodeProductionRowsInvalid = "PRD-OPB-001"
	CodeProductionRowsTaken   = "PRD-OPB-004"
	CodePayrollRowsInvalid    = "PAY-OPB-001"
	CodePayrollRowsTaken      = "PAY-OPB-004"
)

// Staged is the opening-balance client of one target, assets, production or payroll.
type Staged struct {
	c        *Receivables
	target   string
	path     string
	postPath string
	dutyPut  string
	dutyGet  string
	dutyPost string
	invalid  string
	taken    string
}

// NewAssets builds the assets client. A zero Config is valid and reports Configured false.
func NewAssets(cfg Config) *Staged {
	c := NewReceivables(cfg)
	c.aud = audAssets
	return &Staged{c: c, target: domain.TargetAssets, path: pathAssetsOpening, postPath: pathAssetsPost, dutyPut: dutyAssetsImport, dutyGet: dutyAssetsImportRead, dutyPost: dutyAssetsPost,
		invalid: CodeAssetsRowsInvalid, taken: CodeAssetsRowsTaken}
}

// NewProduction builds the production client. A zero Config is valid and reports Configured false.
func NewProduction(cfg Config) *Staged {
	c := NewReceivables(cfg)
	c.aud = audProduction
	return &Staged{c: c, target: domain.TargetProduction, path: pathProductionOpening, postPath: pathProductionPost, dutyPut: dutyProductionImport, dutyGet: dutyProductionRead,
		dutyPost: dutyProductionPost, invalid: CodeProductionRowsInvalid, taken: CodeProductionRowsTaken}
}

// NewPayroll builds the payroll client. A zero Config is valid and reports Configured false.
func NewPayroll(cfg Config) *Staged {
	c := NewReceivables(cfg)
	c.aud = audPayroll
	return &Staged{c: c, target: domain.TargetPayroll, path: pathPayrollOpening, dutyPut: dutyPayrollImport, dutyGet: dutyPayrollRead, invalid: CodePayrollRowsInvalid, taken: CodePayrollRowsTaken}
}

// Configured reports whether a load can be attempted: an address and a signing key.
func (t *Staged) Configured() bool { return t != nil && t.c.Configured() }

type stagedBatch struct {
	BatchKey  string            `json:"batchKey"`
	LineCount int               `json:"lineCount"`
	Totals    map[string]string `json:"totals"`
}

// LoadBatch sends one batch of 1 to 1000 lines; extra carries the target's own request fields (openingDate and journal for assets, postingDate for
// production). A refusal for lines (the target's rows-invalid code, or its taken code for a reference another batch imported) is a *RowsRefused;
// any other 4xx is ErrRefused.
func (t *Staged) LoadBatch(ctx context.Context, tenantID, legalEntityID, batchKey string, extra map[string]any, lines []domain.WireLine) (OpeningBatchResult, error) {
	if !t.Configured() {
		return OpeningBatchResult{}, ErrNotConfigured
	}
	if len(lines) == 0 || len(lines) > domain.MaxStagedBatchLines {
		return OpeningBatchResult{}, fmt.Errorf("a batch holds 1 to %d lines, got %d", domain.MaxStagedBatchLines, len(lines))
	}
	body := map[string]any{"legalEntityId": legalEntityID, "batchKey": batchKey}
	for k, v := range extra {
		body[k] = v
	}
	wire := make([]map[string]any, len(lines))
	totals := map[string]decimal.Decimal{}
	for i, l := range lines {
		wire[i] = l.Body
		for k, v := range l.Totals {
			totals[k] = totals[k].Add(v)
		}
	}
	control := make(map[string]string, len(totals))
	for k, v := range totals {
		control[k] = v.String()
	}
	body["controlTotals"], body["lines"] = control, wire
	payload, err := json.Marshal(body)
	if err != nil {
		return OpeningBatchResult{}, err
	}
	status, answer, err := t.c.do(ctx, http.MethodPost, t.path, tenantID, legalEntityID, t.dutyPut, "", payload)
	if err != nil {
		return OpeningBatchResult{}, err
	}
	switch status {
	case http.StatusOK, http.StatusCreated:
		var b stagedBatch
		if json.Unmarshal(answer, &b) != nil || b.BatchKey != batchKey || b.LineCount != len(lines) {
			return OpeningBatchResult{}, &Error{Kind: ErrBadAnswer, Status: status, Detail: "the answer does not describe the batch sent"}
		}
		return OpeningBatchResult{ItemCount: b.LineCount, Replayed: status == http.StatusOK}, nil
	case http.StatusUnprocessableEntity:
		var pb problemBody
		_ = json.Unmarshal(answer, &pb)
		if pb.Code == t.invalid || pb.Code == t.taken {
			if rows := rowErrors(pb.Errors, len(lines)); len(rows) > 0 {
				return OpeningBatchResult{}, &RowsRefused{Code: pb.Code, Rows: rows, Errors: pb.RowErrors}
			}
		}
	}
	return OpeningBatchResult{}, answerError(status, answer)
}

// BatchTotals reads every opening batch the target holds for the legal entity and sums the ones the factory wrote for the staged pack of the company
// (keep reports whether a batch key is one): the number of lines and each named total.
func (t *Staged) BatchTotals(ctx context.Context, tenantID, legalEntityID string, keep func(key string) bool) (domain.StagedTotals, error) {
	sum := domain.NewStagedTotals()
	if !t.Configured() {
		return sum, ErrNotConfigured
	}
	err := readPages(ctx, t.c, t.path, tenantID, legalEntityID, t.dutyGet, func(raw json.RawMessage) error {
		var b stagedBatch
		if err := json.Unmarshal(raw, &b); err != nil {
			return err
		}
		if !keep(b.BatchKey) {
			return nil
		}
		sums := map[string]decimal.Decimal{}
		for name, v := range b.Totals {
			d, err := decimal.NewFromString(v)
			if err != nil {
				return err
			}
			sums[name] = d
		}
		sum.Add(b.LineCount, sums)
		return nil
	})
	return sum, err
}

// JournalResult is a target's answer to the request to book the opening journal.
type JournalResult struct {
	Booked int
	// Failed lists the documents the ledger refused, with the ledger's code.
	Failed []string
	// More is true when the target stopped at its per-request limit and the call is to be repeated.
	More bool
}

// CanPostJournal reports whether the target books an opening journal (assets and production do; payroll holds balances only).
func (t *Staged) CanPostJournal() bool { return t != nil && t.postPath != "" }

// PostJournal asks the target to book the opening transactions it holds in the general ledger. body names the ledger and, for assets, the book and
// currency; one call books up to 200 documents.
func (t *Staged) PostJournal(ctx context.Context, tenantID, legalEntityID string, body map[string]any) (JournalResult, error) {
	if !t.Configured() {
		return JournalResult{}, ErrNotConfigured
	}
	if !t.CanPostJournal() {
		return JournalResult{}, fmt.Errorf("the %s target has no opening journal", t.target)
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return JournalResult{}, err
	}
	status, answer, err := t.c.do(ctx, http.MethodPost, t.postPath, tenantID, legalEntityID, t.dutyPost, "", payload)
	if err != nil {
		return JournalResult{}, err
	}
	if status != http.StatusOK {
		return JournalResult{}, answerError(status, answer)
	}
	var out struct {
		Booked []json.RawMessage `json:"booked"`
		Failed []struct {
			Document string `json:"document"`
			Code     string `json:"code"`
		} `json:"failed"`
		More      bool `json:"more"`
		Remaining int  `json:"remaining"`
	}
	if json.Unmarshal(answer, &out) != nil {
		return JournalResult{}, &Error{Kind: ErrBadAnswer, Status: status, Detail: "the journal answer is not readable"}
	}
	res := JournalResult{Booked: len(out.Booked), More: out.More || out.Remaining > 0}
	for _, f := range out.Failed {
		res.Failed = append(res.Failed, f.Document+" ("+f.Code+")")
	}
	return res, nil
}
