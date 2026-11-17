package statements

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/print/internal/delivery"
	"github.com/BenjaminSRussell/depot/services/print/internal/domain"
	"github.com/BenjaminSRussell/depot/services/print/internal/store"
)

const (
	// Lease is how long a replica holds a due schedule. A run that outlives it can be taken over by another replica; the
	// per-customer claim keeps that from producing a statement twice.
	Lease = 15 * time.Minute
	// Interval is how often the scheduled loop looks for due schedules.
	Interval = 10 * time.Minute
	batch    = 20
	// MaxAttempts is how many passes an unfinished run gets (each retries only the customers that failed) before the schedule moves on.
	MaxAttempts = 5
	// RetryEvery is the wait before a pass after a failed one, times the attempts so far.
	RetryEvery = 15 * time.Minute
	// RetryWait is the wait after the first failed pass (exported for tests).
	RetryWait = RetryEvery
)

// Runner renders and delivers the statements of due schedules. It is safe to run in several replicas at once.
type Runner struct {
	Schedules store.ScheduleStore
	Docs      store.DocStore
	Source    Source
	Deliver   *delivery.Deliverer
	// Owner names this replica in the lease; the host name by default.
	Owner string
	Now   func() time.Time
}

// Summary counts what one schedule's run did.
type Summary struct {
	ScheduleID string `json:"scheduleId"`
	AsOf       string `json:"asOf"`
	Delivered  int    `json:"delivered"`
	Skipped    int    `json:"skipped"`
	Failed     int    `json:"failed"`
	// Retry says the run is not finished: a statement failed and the next pass retries it.
	Retry bool `json:"retry,omitempty"`
}

func (r *Runner) now() time.Time {
	if r.Now != nil {
		return r.Now().UTC()
	}
	return time.Now().UTC()
}

func (r *Runner) owner() string {
	if r.Owner != "" {
		return r.Owner
	}
	h, _ := os.Hostname()
	return h
}

func date(t time.Time) time.Time {
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

// RunTenant claims the tenant's due schedules and runs each. The context carries nothing yet: the runner acts as its own
// service principal for the tenant (and the schedule's legal entity).
func (r *Runner) RunTenant(ctx context.Context, tenantID string) ([]Summary, error) {
	base, err := tenant.ServicePrincipal(Principal, tenantID, "")
	if err != nil {
		return nil, err
	}
	tctx := tenant.With(ctx, base)
	due, err := r.Schedules.ClaimDueSchedules(tctx, r.now(), r.owner(), Lease, batch)
	if err != nil {
		return nil, err
	}
	var out []Summary
	var firstErr error
	for _, s := range due {
		sum, err := r.runSchedule(ctx, tenantID, s, true)
		out = append(out, sum)
		if err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return out, firstErr
}

// RunNow runs one schedule for today whether or not it is due (the "run now" button); a statement already delivered for today
// is not sent again, one that failed is tried again. The schedule's own run date does not move.
func (r *Runner) RunNow(ctx context.Context, tenantID string, s domain.StatementSchedule) (Summary, error) {
	return r.runSchedule(ctx, tenantID, s, false)
}

func (r *Runner) runSchedule(ctx context.Context, tenantID string, s domain.StatementSchedule, scheduled bool) (Summary, error) {
	p, err := tenant.ServicePrincipal(Principal, tenantID, s.LegalEntityID)
	if err != nil {
		return Summary{ScheduleID: s.ID}, err
	}
	ctx = tenant.With(ctx, p)
	today := date(r.now())
	// An unfinished run keeps its date, so the customers already sent are not sent again and the failed ones are retried.
	if s.RunAsOf != "" {
		if d, err := time.Parse(time.DateOnly, s.RunAsOf); err == nil {
			today = d
		}
	}
	sum := Summary{ScheduleID: s.ID, AsOf: today.Format(time.DateOnly)}
	customers, err := r.Source.Customers(ctx, s.LegalEntityID)
	if err != nil {
		// The lease expires and the next pass tries again; the run date has not moved.
		return sum, fmt.Errorf("list customers of schedule %s: %w", s.ID, err)
	}
	for _, c := range customers {
		switch r.one(ctx, s, today, c) {
		case "sent":
			sum.Delivered++
		case "skipped":
			sum.Skipped++
		default:
			sum.Failed++
		}
	}
	if scheduled {
		if sum.Failed > 0 && s.Attempts+1 < MaxAttempts {
			sum.Retry = true
			if err := r.Schedules.DeferSchedule(ctx, s.ID, today, s.Attempts+1, r.now().Add(time.Duration(s.Attempts+1)*RetryEvery)); err != nil {
				return sum, err
			}
			return sum, nil
		}
		if sum.Failed > 0 {
			slog.Error("scheduled statements gave up on failed customers", "schedule", s.ID, "asOf", sum.AsOf, "failed", sum.Failed)
		}
		next := domain.NextRunOn(s.Cadence, s.RunDay, date(r.now()))
		if err := r.Schedules.CompleteSchedule(ctx, s.ID, today, next); err != nil {
			return sum, err
		}
	}
	return sum, nil
}

// one produces one customer's statement and returns "sent", "skipped" or "failed".
func (r *Runner) one(ctx context.Context, s domain.StatementSchedule, asOf time.Time, customerID string) string {
	ok, err := r.Schedules.BeginStatement(ctx, s.ID, asOf, customerID)
	if err != nil {
		slog.Error("statement claim failed", "schedule", s.ID, "err", err)
		return "failed"
	}
	if !ok {
		return "skipped"
	}
	state, detail := r.produce(ctx, s, asOf, customerID)
	if err := r.Schedules.FinishStatement(ctx, s.ID, asOf, customerID, state != "failed", detail); err != nil {
		slog.Error("statement record failed", "schedule", s.ID, "err", err)
		return "failed"
	}
	return state
}

func (r *Runner) produce(ctx context.Context, s domain.StatementSchedule, asOf time.Time, customerID string) (string, string) {
	day := asOf.Format(time.DateOnly)
	sections, err := r.Source.Statement(ctx, s.LegalEntityID, customerID, asOf)
	if err != nil {
		return "failed", err.Error()
	}
	lines := 0
	for _, sec := range sections {
		lines += len(sec.Lines)
	}
	if lines == 0 {
		return "skipped", "nothing open on " + day
	}
	// A customer whose name cannot be read is addressed as "Customer", so a letter text with {{counterpartyName}} still fills in.
	name := strings.TrimSpace(r.Source.CustomerName(ctx, s.LegalEntityID, customerID))
	if name == "" {
		name = "Customer"
	}
	req := domain.StatementDocument(s.SenderName, name, day, sections)
	var tpl *domain.DocumentTemplate
	if s.TemplateCode != "" {
		t, err := r.Docs.GetTemplate(ctx, s.TemplateCode)
		if err != nil {
			return "failed", "template " + s.TemplateCode + ": " + err.Error()
		}
		tpl = &t
	} else if t, err := r.Docs.DefaultTemplate(ctx, domain.DocPeriodicStatement); err != nil {
		return "failed", err.Error()
	} else {
		tpl = t
	}
	if tpl != nil {
		in := domain.ComposeInput{Language: s.Language, Values: map[string]string{"counterpartyName": name, "asOf": day, "statementDate": day, "legalEntityName": s.SenderName}}
		// A template's letter text replaces the standard sentence when the template names a body block.
		if tpl.Config.BodyBlock != "" {
			req.Body = ""
		}
		composed, err := domain.Compose(req, tpl, in, func(code string) ([]domain.TextBlock, error) { return r.Docs.TextBlockVersions(ctx, code) })
		if err != nil {
			return "failed", err.Error()
		}
		req = composed
	}
	doc, err := domain.RenderDocumentReport(req)
	if err != nil {
		return "failed", err.Error()
	}
	results, err := r.Deliver.Deliver(ctx, delivery.Document{
		SourceKey: fmt.Sprintf("statement:%s:%s:%s", s.ID, day, customerID), Type: domain.DocPeriodicStatement, Number: req.Number,
		LegalEntityID: s.LegalEntityID, AccountID: customerID, AccountVerified: true, PDF: doc.PDF, Pages: doc.Pages,
		Subject: "Statement of account as of " + day,
		Body:    "Please find your statement of account as of " + day + " attached.",
	})
	if err != nil {
		return "failed", err.Error()
	}
	if len(results) == 0 {
		return "skipped", "no output rule for statements"
	}
	sent, failed := 0, []string{}
	for _, res := range results {
		switch res.Status {
		case store.DeliverySent:
			sent++
		case store.DeliveryFailed:
			failed = append(failed, res.Channel+": "+res.Detail)
		}
	}
	if len(failed) > 0 {
		return "failed", strings.Join(failed, "; ")
	}
	if sent == 0 {
		return "skipped", "no destination of the plan is delivered by this service"
	}
	return "sent", ""
}

// RunLoop runs the due schedules of each tenant every interval until ctx ends, starting once at once. report is called after every
// pass with what it did. Several replicas may all run it: the lease on a schedule and the claim on each statement keep a statement
// from being produced twice.
func RunLoop(ctx context.Context, r *Runner, tenants []string, interval time.Duration, report func([]Summary, error)) {
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		var all []Summary
		var firstErr error
		for _, id := range tenants {
			sums, err := r.RunTenant(ctx, id)
			all = append(all, sums...)
			if err != nil && firstErr == nil {
				firstErr = err
			}
		}
		report(all, firstErr)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}
