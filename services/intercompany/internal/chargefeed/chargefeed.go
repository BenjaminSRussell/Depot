// Package chargefeed raises intercompany charges from the events of expense and projects (#40). When another legal entity bears the
// cost, the entity that paid or employed is the lender and the bearing entity the borrower:
//
//   - dev.passage.expense.report.approved.v1 lists, in chargedLines, the report lines whose chargeToLegalEntityId differs from the
//     entity that pays the report. Each becomes an Expense charge from the paying entity to the bearing one, quantity 1 at the line
//     amount, keyed expense:<lineId> version 1.
//   - dev.passage.projects.transaction.posted.v1 is published for every HOUR transaction. When it names the worker's employing
//     entity and that differs from the project's entity, it becomes an Hour charge from the employing entity to the project's, the
//     hours at the hourly cost, keyed projects:<transactionId> version 1.
//
// The transfer price is the pair's price for the category with no resource role (Expense or Hour); a pair with none parks the charge
// on suspense (202, IC-0821) until one is set and the next version reprices it. A charge parked on suspense (202) or compensated is not repriced by the same version again: the next source version (an expense report revision) does it, and a compensated one is dead-lettered. Both go through the same POST /v1/interco/charges logic as a person's request (transfer price in force, tax, settlement mode), so a
// charge is stored once per sourceId and version: a redelivered event replays the stored charge and posts nothing again, and a line
// or transaction is never charged twice. An event whose entities are not in the registry, or that is already the paying entity's own
// cost, is skipped with a log line. The consumer runs as service:intercompany-charges (one flag duty) scoped to the two entities.
package chargefeed

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/eventing"
	"github.com/BenjaminSRussell/passage/platform/events"
	"github.com/BenjaminSRussell/passage/platform/ids"
	"github.com/BenjaminSRussell/passage/platform/reimbursement"
	"github.com/BenjaminSRussell/passage/platform/tenant"

	"github.com/BenjaminSRussell/passage/services/intercompany/internal/domain"
)

const (
	// ConsumerName is the consumer (and Kafka group) and the service principal it runs as, service:intercompany-charges.
	ConsumerName = "intercompany-charges"
	// Duty is the only duty of that principal.
	Duty            = "intercompany.charge.from-event"
	DeadLetterTopic = "passage.intercompany.charges.dlq"
	ExpenseTopic    = "passage.expense.report"
	ProjectsTopic   = "passage.projects.transaction"
	// ExpenseSource and ProjectsSource are the CloudEvents sources the two services publish with.
	ExpenseSource  = "/passage/expense/expense"
	ProjectsSource = "projects"
	// TransactionPostedType is projects' HOUR transaction event.
	TransactionPostedType = "dev.passage.projects.transaction.posted.v1"
)

// Submitter runs a charge request the way POST /v1/interco/charges does; *api.API implements it.
type Submitter interface {
	SubmitCharge(ctx context.Context, body []byte) (int, []byte)
}

// Entities lists the registry; the store implements it. It maps a legal entity to the entity code charges are made between.
type Entities interface {
	ListEntities(ctx context.Context) ([]domain.EntityRecord, error)
}

// Config holds the accounts the Journal-mode legs of a charge post to; Invoice mode ignores them.
type Config struct {
	CostAccount     string
	RecoveryAccount string
	MarginAccount   string
}

// Validate reports a configuration that cannot post a charge.
func (c Config) Validate() error {
	if c.CostAccount == "" || c.RecoveryAccount == "" || c.MarginAccount == "" {
		return errors.New("IC_CHARGE_COST_ACCOUNT, IC_CHARGE_RECOVERY_ACCOUNT and IC_CHARGE_MARGIN_ACCOUNT are required")
	}
	return nil
}

// Request is the body of POST /v1/interco/charges the consumer sends.
type Request struct {
	SourceID        string `json:"sourceId"`
	Version         int    `json:"version"`
	LendingEntity   string `json:"lendingEntity"`
	BorrowingEntity string `json:"borrowingEntity"`
	Category        string `json:"category"`
	ResourceRole    string `json:"resourceRole,omitempty"`
	TransactionDate string `json:"transactionDate"`
	// PostingDate is the transaction date: the poster moves a side whose period is closed to the first open one.
	PostingDate     string `json:"postingDate"`
	Quantity        string `json:"quantity"`
	UnitCost        string `json:"unitCost"`
	Currency        string `json:"currency"`
	CostAccount     string `json:"costAccount"`
	RecoveryAccount string `json:"recoveryAccount"`
	MarginAccount   string `json:"marginAccount"`
}

// Feed turns the events into charges.
type Feed struct {
	Submit   Submitter
	Entities Entities
	Config   Config
}

// Handlers returns the consumer's handlers. Register them with a consumer named ConsumerName with Idempotent set.
func (f *Feed) Handlers() map[string]eventing.Handler {
	return map[string]eventing.Handler{
		reimbursement.ReportApprovedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return f.HandleExpense(ctx, evt)
		}),
		TransactionPostedType: eventing.HandlerFunc(func(ctx context.Context, _ pgx.Tx, evt events.Event) error {
			return f.HandleHours(ctx, evt)
		}),
	}
}

// HandleExpense raises one Expense charge per charged line of an approved report.
func (f *Feed) HandleExpense(ctx context.Context, evt events.Event) error {
	if err := checkSource(ctx, evt, ExpenseSource); err != nil {
		return err
	}
	var d reimbursement.ReportApproved
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("report.approved data: %w", err))
	}
	for _, l := range d.ChargedLines {
		amount, err := decimal.NewFromString(l.Amount)
		if err != nil || !amount.IsPositive() {
			return eventing.Permanent(fmt.Errorf("line %s of report %s has no positive amount", l.LineID, d.ReportID))
		}
		if !ids.Valid(d.LegalEntityID) || !ids.Valid(l.ChargeToLegalEntityID) {
			return eventing.Permanent(fmt.Errorf("line %s of report %s names no legal entities", l.LineID, d.ReportID))
		}
		if strings.EqualFold(d.LegalEntityID, l.ChargeToLegalEntityID) {
			continue
		}
		// The report's revision is the charge's version: a revised report reprices the line with the next version.
		req := Request{SourceID: "expense:" + l.LineID, Version: max(d.Revision, 1), Category: "Expense", TransactionDate: l.TransactionDate,
			Quantity: "1", UnitCost: amount.String(), Currency: l.Currency}
		if err := f.charge(ctx, evt.TenantID, d.LegalEntityID, l.ChargeToLegalEntityID, req); err != nil {
			return err
		}
	}
	return nil
}

// PostedHours is the part of projects' transaction.posted event this consumer reads.
type PostedHours struct {
	TransactionID        string `json:"transactionId"`
	Version              int    `json:"version"`
	ProjectLegalEntityID string `json:"projectLegalEntityId"`
	WorkerLegalEntityID  string `json:"workerLegalEntityId"`
	CategoryID           string `json:"categoryId"`
	TransactionDate      string `json:"transactionDate"`
	Quantity             string `json:"quantity"`
	CostPrice            string `json:"costPrice"`
	Currency             string `json:"currency"`
}

// HandleHours raises the Hour charge of a project transaction whose worker is employed by another entity than the project's.
func (f *Feed) HandleHours(ctx context.Context, evt events.Event) error {
	if err := checkSource(ctx, evt, ProjectsSource); err != nil {
		return err
	}
	var d PostedHours
	if err := json.Unmarshal(evt.Data, &d); err != nil {
		return eventing.Permanent(fmt.Errorf("transaction.posted data: %w", err))
	}
	if d.WorkerLegalEntityID == "" || strings.EqualFold(d.WorkerLegalEntityID, d.ProjectLegalEntityID) {
		return nil // the source did not name an employing entity, or the hours are the project entity's own
	}
	hours, err := decimal.NewFromString(d.Quantity)
	if err != nil || d.Version < 1 || !ids.Valid(d.TransactionID) || !ids.Valid(d.WorkerLegalEntityID) || !ids.Valid(d.ProjectLegalEntityID) {
		return eventing.Permanent(fmt.Errorf("transaction %s cannot be charged: no hours, version or legal entities", d.TransactionID))
	}
	if !hours.IsPositive() {
		return nil
	}
	req := Request{SourceID: "projects:" + d.TransactionID, Version: d.Version, Category: "Hour", TransactionDate: d.TransactionDate,
		Quantity: hours.String(), UnitCost: d.CostPrice, Currency: d.Currency}
	return f.charge(ctx, evt.TenantID, d.WorkerLegalEntityID, d.ProjectLegalEntityID, req)
}

func checkSource(ctx context.Context, evt events.Event, want string) error {
	actor, ok := tenant.FromContext(ctx)
	if !ok || !actor.HasDuty(Duty) {
		return eventing.Permanent(errors.New("ic charge feed has no principal with " + Duty))
	}
	if evt.Source != want {
		return eventing.Permanent(fmt.Errorf("%s from source %q, want %q", evt.Type, evt.Source, want))
	}
	return nil
}

// charge maps the two legal entities to registry entity codes and submits the charge as a principal that covers both.
func (f *Feed) charge(ctx context.Context, tenantID, lenderLE, borrowerLE string, req Request) error {
	if err := f.Config.Validate(); err != nil {
		return eventing.Permanent(err)
	}
	principal, err := tenant.ServicePrincipal(ConsumerName, tenantID, lenderLE)
	if err != nil {
		return eventing.Permanent(err)
	}
	principal.LegalEntityID = strings.ToLower(lenderLE) + "," + strings.ToLower(borrowerLE)
	ctx = tenant.With(ctx, principal)
	list, err := f.Entities.ListEntities(ctx)
	if err != nil {
		return err
	}
	for _, e := range list {
		switch {
		case strings.EqualFold(e.LegalEntityID, lenderLE) && req.LendingEntity == "":
			req.LendingEntity = e.Entity
		case strings.EqualFold(e.LegalEntityID, borrowerLE) && req.BorrowingEntity == "":
			req.BorrowingEntity = e.Entity
		}
	}
	if req.LendingEntity == "" || req.BorrowingEntity == "" {
		// Nothing is charged for an entity the registry does not know: the event is dead-lettered, so the cost is not silently left with
		// the payer; once the entity is registered the event is replayed.
		return eventing.Permanent(fmt.Errorf("charge %s: legal entity %s or %s is not in the intercompany registry", req.SourceID, lenderLE, borrowerLE))
	}
	req.PostingDate = req.TransactionDate
	req.CostAccount, req.RecoveryAccount, req.MarginAccount = f.Config.CostAccount, f.Config.RecoveryAccount, f.Config.MarginAccount
	body, err := json.Marshal(req)
	if err != nil {
		return eventing.Permanent(err)
	}
	status, resp := f.Submit.SubmitCharge(ctx, body)
	switch {
	case status == http.StatusCreated:
		var out struct {
			Status string `json:"status"`
		}
		if json.Unmarshal(resp, &out) == nil && out.Status == "compensated" {
			// Both sides were reversed: the charge is not done. A replay of the same version answers the same, so a person re-drives it
			// with the next version (a revised report or a new source version).
			return eventing.Permanent(fmt.Errorf("charge %s was compensated: its postings were reversed", req.SourceID))
		}
		return nil
	case status == http.StatusAccepted:
		slog.WarnContext(ctx, "intercompany charge parked on suspense: no transfer price in force", "source", req.SourceID, "lender", req.LendingEntity, "borrower", req.BorrowingEntity)
		return nil
	case status >= 500 || status == http.StatusTooManyRequests:
		return fmt.Errorf("charge %s: status %d", req.SourceID, status)
	default:
		return eventing.Permanent(fmt.Errorf("charge %s refused: status %d %s", req.SourceID, status, strings.TrimSpace(string(resp))))
	}
}
