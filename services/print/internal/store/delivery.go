package store

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/print/internal/domain"
)

const (
	DeliveryPending = "PENDING"
	DeliverySent    = "SENT"
	DeliverySkipped = "SKIPPED"
	DeliveryFailed  = "FAILED"

	CodePortalDocumentNotFound = "PRN-0020" // portal document not found (service-local)
)

// Delivery is the record of one destination of one delivered document.
type Delivery struct {
	ID          string `json:"id"`
	SourceKey   string `json:"sourceKey"`
	RuleID      string `json:"ruleId"`
	CopyKind    string `json:"copyKind"`
	Channel     string `json:"channel"`
	Destination string `json:"destination,omitempty"`
	Status      string `json:"status"`
	Detail      string `json:"detail,omitempty"`
}

// PortalDocument is a PDF offered to a customer in the customer portal.
type PortalDocument struct {
	ID            string    `json:"id"`
	SourceKey     string    `json:"-"`
	AccountID     string    `json:"accountId"`
	LegalEntityID string    `json:"legalEntityId,omitempty"`
	DocumentType  string    `json:"documentType"`
	Number        string    `json:"number"`
	Pages         int       `json:"pages"`
	CreatedAt     time.Time `json:"createdAt"`
	PDF           []byte    `json:"-"`
}

// DeliveryStore records deliveries and keeps the documents of the portal destination. Postgres implements it next to Store.
type DeliveryStore interface {
	// ClaimDelivery records the intent to deliver a document to one rule's destination. claimed is false when that destination
	// already took the document (SENT or SKIPPED) or another replica is on it (PENDING, younger than a minute), so a repeated
	// delivery sends nothing twice; a FAILED or stale PENDING row is claimed again.
	ClaimDelivery(ctx context.Context, d Delivery) (Delivery, bool, error)
	FinishDelivery(ctx context.Context, id, status, detail string) error
	ListDeliveries(ctx context.Context, sourceKey string) ([]Delivery, error)
	// SavePortalDocument stores the PDF for the portal; a repeated source key returns the stored document, not a second one.
	SavePortalDocument(ctx context.Context, d PortalDocument) (PortalDocument, error)
	ListPortalDocuments(ctx context.Context, accountID string, limit int) ([]PortalDocument, error)
	GetPortalDocument(ctx context.Context, id, accountID string) (PortalDocument, error)
}

var _ DeliveryStore = (*Postgres)(nil)

const staleClaim = time.Minute

// ClaimDelivery: see DeliveryStore.
func (s *Postgres) ClaimDelivery(ctx context.Context, d Delivery) (Delivery, bool, error) {
	p, _ := tenant.FromContext(ctx)
	var out Delivery
	var claimed bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		out = d
		out.Status = DeliveryPending
		err := tx.QueryRow(ctx, `insert into print_delivery (tenant_id, id, source_key, rule_id, copy_kind, channel, destination, status)
			values ($1, $2, $3, $4, $5, $6, $7, 'PENDING') on conflict (tenant_id, source_key, rule_id) do nothing returning id::text`,
			p.TenantID, ids.New(), d.SourceKey, d.RuleID, d.CopyKind, d.Channel, d.Destination).Scan(&out.ID)
		if err == nil {
			claimed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		err = tx.QueryRow(ctx, `update print_delivery set status = 'PENDING', detail = '', updated_at = now()
			where tenant_id = $1 and source_key = $2 and rule_id = $3
			  and (status = 'FAILED' or (status = 'PENDING' and updated_at < now() - $4::interval))
			returning id::text`, p.TenantID, d.SourceKey, d.RuleID, staleClaim.String()).Scan(&out.ID)
		if err == nil {
			claimed = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		return tx.QueryRow(ctx, `select id::text, status, detail from print_delivery where tenant_id = $1 and source_key = $2 and rule_id = $3`,
			p.TenantID, d.SourceKey, d.RuleID).Scan(&out.ID, &out.Status, &out.Detail)
	})
	return out, claimed, err
}

// FinishDelivery records the outcome of a claimed delivery.
func (s *Postgres) FinishDelivery(ctx context.Context, id, status, detail string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update print_delivery set status = $3, detail = $4, updated_at = now() where tenant_id = $1 and id = $2`, p.TenantID, id, status, detail)
		return err
	})
}

// ListDeliveries returns the destinations tried for one document.
func (s *Postgres) ListDeliveries(ctx context.Context, sourceKey string) ([]Delivery, error) {
	p, _ := tenant.FromContext(ctx)
	out := []Delivery{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select id::text, source_key, rule_id::text, copy_kind, channel, destination, status, detail from print_delivery
			where tenant_id = $1 and source_key = $2 order by created_at, id`, p.TenantID, sourceKey)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var d Delivery
			if err := rows.Scan(&d.ID, &d.SourceKey, &d.RuleID, &d.CopyKind, &d.Channel, &d.Destination, &d.Status, &d.Detail); err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

const portalColumns = "id::text, source_key, account_id::text, coalesce(legal_entity_id::text, ''), document_type, number, pages, created_at"

func scanPortal(row pgx.Row, withPDF bool) (PortalDocument, error) {
	var d PortalDocument
	dest := []any{&d.ID, &d.SourceKey, &d.AccountID, &d.LegalEntityID, &d.DocumentType, &d.Number, &d.Pages, &d.CreatedAt}
	if withPDF {
		dest = append(dest, &d.PDF)
	}
	err := row.Scan(dest...)
	d.CreatedAt = d.CreatedAt.UTC()
	return d, err
}

// SavePortalDocument: see DeliveryStore.
func (s *Postgres) SavePortalDocument(ctx context.Context, d PortalDocument) (PortalDocument, error) {
	p, _ := tenant.FromContext(ctx)
	var out PortalDocument
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanPortal(tx.QueryRow(ctx, `insert into print_portal_document (tenant_id, id, source_key, account_id, legal_entity_id, document_type, number, pages, pdf)
			values ($1, $2, $3, $4, nullif($5, '')::uuid, $6, $7, $8, $9)
			on conflict (tenant_id, source_key) do nothing
			returning `+portalColumns, p.TenantID, ids.New(), d.SourceKey, d.AccountID, d.LegalEntityID, d.DocumentType, d.Number, d.Pages, d.PDF), false)
		if errors.Is(err, pgx.ErrNoRows) {
			out, err = scanPortal(tx.QueryRow(ctx, `select `+portalColumns+` from print_portal_document where tenant_id = $1 and source_key = $2`, p.TenantID, d.SourceKey), false)
		}
		return err
	})
	return out, err
}

// ListPortalDocuments returns the documents of one customer, newest first.
func (s *Postgres) ListPortalDocuments(ctx context.Context, accountID string, limit int) ([]PortalDocument, error) {
	p, _ := tenant.FromContext(ctx)
	if limit < 1 || limit > 200 {
		limit = 100
	}
	out := []PortalDocument{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+portalColumns+` from print_portal_document where tenant_id = $1 and account_id = $2 order by created_at desc, id limit $3`, p.TenantID, accountID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			d, err := scanPortal(rows, false)
			if err != nil {
				return err
			}
			out = append(out, d)
		}
		return rows.Err()
	})
	return out, err
}

// GetPortalDocument returns one document with its PDF, only when it belongs to the account (404 otherwise).
func (s *Postgres) GetPortalDocument(ctx context.Context, id, accountID string) (PortalDocument, error) {
	p, _ := tenant.FromContext(ctx)
	var out PortalDocument
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanPortal(tx.QueryRow(ctx, `select `+portalColumns+`, pdf from print_portal_document where tenant_id = $1 and id = $2 and account_id = $3`, p.TenantID, id, accountID), true)
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return PortalDocument{}, httpx.NewProblem(http.StatusNotFound, CodePortalDocumentNotFound, "document not found")
	}
	return out, err
}

// ScheduleStore keeps statement schedules and their runs. Postgres implements it next to Store.
type ScheduleStore interface {
	CreateSchedule(ctx context.Context, s domain.StatementSchedule, idempotencyKey string) (domain.StatementSchedule, bool, error)
	ListSchedules(ctx context.Context) ([]domain.StatementSchedule, error)
	SetScheduleEnabled(ctx context.Context, id string, enabled bool) (domain.StatementSchedule, error)
	DeleteSchedule(ctx context.Context, id string) error
	// ClaimDueSchedules leases the enabled schedules whose run date has come (next_run_on <= the date of now, and the retry wait of an unfinished run is over) and that no replica holds,
	// for lease; two replicas calling at once receive disjoint sets.
	ClaimDueSchedules(ctx context.Context, today time.Time, owner string, lease time.Duration, limit int) ([]domain.StatementSchedule, error)
	// CompleteSchedule releases the lease and moves the schedule to its next run date.
	CompleteSchedule(ctx context.Context, id string, ranOn, next time.Time) error
	// DeferSchedule keeps an unfinished run (a customer failed): the run date stays due with its as-of date, attempts rises and the
	// schedule is not offered again before retryAfter. The lease is released.
	DeferSchedule(ctx context.Context, id string, asOf time.Time, attempts int, retryAfter time.Time) error
	// BeginStatement claims one customer's statement of a run date: false when it is already DONE or another run holds it.
	BeginStatement(ctx context.Context, scheduleID string, asOf time.Time, customerID string) (bool, error)
	FinishStatement(ctx context.Context, scheduleID string, asOf time.Time, customerID string, ok bool, detail string) error
	ListStatementRuns(ctx context.Context, scheduleID string, limit int) ([]StatementRun, error)
}

// StatementRun is one customer's statement of one run date.
type StatementRun struct {
	ScheduleID string `json:"scheduleId"`
	AsOf       string `json:"asOf"`
	CustomerID string `json:"customerId"`
	Status     string `json:"status"`
	Detail     string `json:"detail,omitempty"`
}

var _ ScheduleStore = (*Postgres)(nil)

const scheduleColumns = `id::text, name, legal_entity_id::text, sender_name, cadence, run_day, language, template_code, enabled,
	to_char(next_run_on, 'YYYY-MM-DD'), coalesce(to_char(last_run_on, 'YYYY-MM-DD'), ''),
	coalesce(to_char(run_as_of, 'YYYY-MM-DD'), ''), attempts`

func scanSchedule(row pgx.Row) (domain.StatementSchedule, error) {
	var s domain.StatementSchedule
	err := row.Scan(&s.ID, &s.Name, &s.LegalEntityID, &s.SenderName, &s.Cadence, &s.RunDay, &s.Language, &s.TemplateCode, &s.Enabled, &s.NextRunOn, &s.LastRunOn, &s.RunAsOf, &s.Attempts)
	return s, err
}

func scheduleNotFound() error {
	return httpx.NewProblem(http.StatusNotFound, domain.CodeScheduleNotFound, "statement schedule not found")
}

// CreateSchedule stores a schedule whose first run is the first matching day after today. A repeated Idempotency-Key returns
// the stored schedule (inserted=false); another body under it is refused.
func (s *Postgres) CreateSchedule(ctx context.Context, in domain.StatementSchedule, key string) (domain.StatementSchedule, bool, error) {
	p, _ := tenant.FromContext(ctx)
	first := domain.NextRunOn(in.Cadence, in.RunDay, time.Now().UTC())
	var out domain.StatementSchedule
	var inserted bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanSchedule(tx.QueryRow(ctx, `insert into print_statement_schedule (tenant_id, id, name, legal_entity_id, sender_name, cadence, run_day, language, template_code, next_run_on, idempotency_key)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11) on conflict (tenant_id, idempotency_key) do nothing returning `+scheduleColumns,
			p.TenantID, ids.New(), in.Name, in.LegalEntityID, in.SenderName, in.Cadence, in.RunDay, in.Language, in.TemplateCode, first, key))
		if err == nil {
			inserted = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out, err = scanSchedule(tx.QueryRow(ctx, `select `+scheduleColumns+` from print_statement_schedule where tenant_id = $1 and idempotency_key = $2`, p.TenantID, key))
		return err
	})
	if err != nil {
		return domain.StatementSchedule{}, false, err
	}
	if !inserted && (out.Name != in.Name || out.LegalEntityID != in.LegalEntityID || out.SenderName != in.SenderName || out.Cadence != in.Cadence ||
		out.RunDay != in.RunDay || out.Language != in.Language || out.TemplateCode != in.TemplateCode) {
		return domain.StatementSchedule{}, false, httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotencyReuse, "Idempotency-Key was already used with a different request body")
	}
	return out, inserted, nil
}

// ListSchedules returns the tenant's schedules by name.
func (s *Postgres) ListSchedules(ctx context.Context) ([]domain.StatementSchedule, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.StatementSchedule{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+scheduleColumns+` from print_statement_schedule where tenant_id = $1 order by name, id`, p.TenantID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sc, err := scanSchedule(rows)
			if err != nil {
				return err
			}
			out = append(out, sc)
		}
		return rows.Err()
	})
	return out, err
}

// SetScheduleEnabled pauses or resumes a schedule. Resuming moves a run date in the past to the next matching day, so a
// schedule paused for months does not run once for each missed date.
func (s *Postgres) SetScheduleEnabled(ctx context.Context, id string, enabled bool) (domain.StatementSchedule, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.StatementSchedule
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		cur, err := scanSchedule(tx.QueryRow(ctx, `select `+scheduleColumns+` from print_statement_schedule where tenant_id = $1 and id = $2 for update`, p.TenantID, id))
		if err != nil {
			return err
		}
		next := cur.NextRunOn
		today := time.Now().UTC().Format(time.DateOnly)
		if enabled && next <= today {
			next = domain.NextRunOn(cur.Cadence, cur.RunDay, time.Now().UTC()).Format(time.DateOnly)
		}
		out, err = scanSchedule(tx.QueryRow(ctx, `update print_statement_schedule set enabled = $3, next_run_on = $4 where tenant_id = $1 and id = $2 returning `+scheduleColumns, p.TenantID, id, enabled, next))
		return err
	})
	if errors.Is(err, pgx.ErrNoRows) || isInvalidUUID(err) {
		return domain.StatementSchedule{}, scheduleNotFound()
	}
	return out, err
}

// DeleteSchedule removes a schedule and its run records.
func (s *Postgres) DeleteSchedule(ctx context.Context, id string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `delete from print_statement_schedule where tenant_id = $1 and id = $2`, p.TenantID, id)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return scheduleNotFound()
		}
		return nil
	})
}

// ClaimDueSchedules: see ScheduleStore.
func (s *Postgres) ClaimDueSchedules(ctx context.Context, today time.Time, owner string, lease time.Duration, limit int) ([]domain.StatementSchedule, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.StatementSchedule{}
	// The runner claims due schedules across every legal entity of the tenant (each run then acts within its schedule's
	// legal entity), so this one statement reads past the legal-entity policy.
	err := pgx.BeginFunc(ctx, s.pool, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `select set_config('depot.tenant_id', $1, true), set_config('depot.legal_entity_scopes', '*', true)`, p.TenantID); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `update print_statement_schedule set claimed_by = $3, claimed_until = now() + $4::interval
			where tenant_id = $1 and id in (
			  select id from print_statement_schedule
			  where tenant_id = $1 and enabled and next_run_on <= ($2::timestamptz at time zone 'UTC')::date and (claimed_until is null or claimed_until < now()) and (retry_after is null or retry_after <= $2::timestamptz)
			  order by next_run_on, id limit $5 for update skip locked)
			returning `+scheduleColumns, p.TenantID, today, owner, lease.String(), limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			sc, err := scanSchedule(rows)
			if err != nil {
				return err
			}
			out = append(out, sc)
		}
		return rows.Err()
	})
	return out, err
}

// CompleteSchedule: see ScheduleStore.
func (s *Postgres) CompleteSchedule(ctx context.Context, id string, ranOn, next time.Time) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update print_statement_schedule set last_run_on = $3, next_run_on = $4, run_as_of = null, attempts = 0, retry_after = null, claimed_by = '', claimed_until = null where tenant_id = $1 and id = $2`,
			p.TenantID, id, ranOn, next)
		return err
	})
}

// BeginStatement: see ScheduleStore.
func (s *Postgres) BeginStatement(ctx context.Context, scheduleID string, asOf time.Time, customerID string) (bool, error) {
	p, _ := tenant.FromContext(ctx)
	var ok bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var id string
		err := tx.QueryRow(ctx, `insert into print_statement_run (tenant_id, id, schedule_id, as_of, customer_id, status) values ($1, $2, $3, $4, $5, 'PENDING')
			on conflict (tenant_id, schedule_id, as_of, customer_id) do nothing returning id::text`, p.TenantID, ids.New(), scheduleID, asOf, customerID).Scan(&id)
		if err == nil {
			ok = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		// A failed statement is tried again by the next run of the same date; one still pending for over 10 minutes is taken over.
		err = tx.QueryRow(ctx, `update print_statement_run set status = 'PENDING', detail = '', updated_at = now()
			where tenant_id = $1 and schedule_id = $2 and as_of = $3 and customer_id = $4
			  and (status = 'FAILED' or (status = 'PENDING' and updated_at < now() - interval '10 minutes')) returning id::text`,
			p.TenantID, scheduleID, asOf, customerID).Scan(&id)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		ok = err == nil
		return err
	})
	return ok, err
}

// FinishStatement: see ScheduleStore.
func (s *Postgres) FinishStatement(ctx context.Context, scheduleID string, asOf time.Time, customerID string, ok bool, detail string) error {
	p, _ := tenant.FromContext(ctx)
	status := "DONE"
	if !ok {
		status = "FAILED"
	}
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update print_statement_run set status = $5, detail = $6, updated_at = now() where tenant_id = $1 and schedule_id = $2 and as_of = $3 and customer_id = $4`,
			p.TenantID, scheduleID, asOf, customerID, status, detail)
		return err
	})
}

// ListStatementRuns returns the newest statement records of a schedule.
func (s *Postgres) ListStatementRuns(ctx context.Context, scheduleID string, limit int) ([]StatementRun, error) {
	p, _ := tenant.FromContext(ctx)
	if limit < 1 || limit > 500 {
		limit = 100
	}
	out := []StatementRun{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select schedule_id::text, to_char(as_of, 'YYYY-MM-DD'), customer_id::text, status, detail from print_statement_run
			where tenant_id = $1 and schedule_id = $2 order by as_of desc, created_at desc, id limit $3`, p.TenantID, scheduleID, limit)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r StatementRun
			if err := rows.Scan(&r.ScheduleID, &r.AsOf, &r.CustomerID, &r.Status, &r.Detail); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}

func isInvalidUUID(err error) bool {
	var pg interface{ SQLState() string }
	return errors.As(err, &pg) && pg.SQLState() == "22P02"
}

// DeferSchedule: see ScheduleStore.
func (s *Postgres) DeferSchedule(ctx context.Context, id string, asOf time.Time, attempts int, retryAfter time.Time) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `update print_statement_schedule set run_as_of = $3, attempts = $4, retry_after = $5, claimed_by = '', claimed_until = null where tenant_id = $1 and id = $2`,
			p.TenantID, id, asOf, attempts, retryAfter)
		return err
	})
}
