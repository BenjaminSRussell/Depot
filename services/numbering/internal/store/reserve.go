package store

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/outbox"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/numbering/internal/domain"
)

const (
	confirmedType    = "dev.depot.numbering.reservation.confirmed.v1"
	voidedType       = "dev.depot.numbering.reservation.voided.v1"
	reservationCols  = `id, sequence_code, legal_entity_id, value, formatted, status, lease_expires_at, coalesce(document_ref,''), coalesce(void_reason,'')`
	forSequenceMatch = `legal_entity_id = $4 and sequence_code = $5`
	forIDMatch       = `id = $4`
)

// Reservation is a number held for a caller until it confirms or voids it.
type Reservation struct {
	ID             string    `json:"id"`
	Sequence       string    `json:"sequence"`
	LegalEntityID  string    `json:"legalEntityId"`
	Value          int64     `json:"value"`
	Formatted      string    `json:"formatted"`
	Status         string    `json:"status"`
	LeaseExpiresAt time.Time `json:"leaseExpiresAt"`
	DocumentRef    string    `json:"documentRef,omitempty"`
	VoidReason     string    `json:"voidReason,omitempty"`
}

// VoidedNumber is one entry of the voided-number log.
type VoidedNumber struct {
	ID        string    `json:"id"`
	Value     int64     `json:"value"`
	Formatted string    `json:"formatted"`
	Reason    string    `json:"reason"`
	VoidedAt  time.Time `json:"voidedAt"`
}

func scanReservation(row pgx.Row) (Reservation, error) {
	var r Reservation
	err := row.Scan(&r.ID, &r.Sequence, &r.LegalEntityID, &r.Value, &r.Formatted, &r.Status, &r.LeaseExpiresAt, &r.DocumentRef, &r.VoidReason)
	return r, err
}

func mapUniqueViolation(err error) error {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == uniqueViolation {
		return httpx.NewProblem(http.StatusConflict, CodeDuplicate, "reservation conflict")
	}
	return err
}

func enqueueReservationEvent(ctx context.Context, tx pgx.Tx, p tenant.Principal, eventType string, r Reservation, extra map[string]any) error {
	data := map[string]any{"reservationId": r.ID, "sequence": r.Sequence, "value": r.Value, "formatted": r.Formatted, "legalEntityId": r.LegalEntityID}
	for k, v := range extra {
		data[k] = v
	}
	evt, err := events.New(eventType, "depot/numbering", r.Formatted, p.TenantID, data)
	if err != nil {
		return err
	}
	return outbox.Enqueue(ctx, tx, "sequence", r.Sequence, evt)
}

func expireLeases(ctx context.Context, tx pgx.Tx, p tenant.Principal, now time.Time, match string, arg ...any) error {
	rows, err := tx.Query(ctx, `update number_reservation set status='voided', void_reason=$2, voided_at=$3
		where tenant_id=$1 and status='reserved' and lease_expires_at <= $3 and `+match+` returning `+reservationCols,
		append([]any{p.TenantID, domain.VoidLeaseExpired, now}, arg...)...)
	if err != nil {
		return err
	}
	expired, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (Reservation, error) { return scanReservation(row) })
	if err != nil {
		return err
	}
	for _, r := range expired {
		if err := enqueueReservationEvent(ctx, tx, p, voidedType, r, map[string]any{"reason": domain.VoidLeaseExpired}); err != nil {
			return err
		}
	}
	return nil
}

// Reserve takes the next number of a sequence and holds it until Confirm, Void or lease expiry.
func (s *Postgres) Reserve(ctx context.Context, code, idempotencyKey string, lease time.Duration) (Reservation, error) {
	p, _ := tenant.FromContext(ctx)
	now := s.clock()
	var out Reservation
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idempotencyKey != "" {
			replay, err := scanReservation(tx.QueryRow(ctx, `select `+reservationCols+` from number_reservation where tenant_id=$1 and idempotency_key=$2`, p.TenantID, idempotencyKey))
			if err == nil {
				if replay.Sequence != code {
					return httpx.NewProblem(http.StatusUnprocessableEntity, CodeIdempotency, "Idempotency-Key was already used with a different request")
				}
				out = replay
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		seq, err := loadForUpdate(ctx, tx, p, code)
		if err != nil {
			return err
		}
		if err := expireLeases(ctx, tx, p, now, forSequenceMatch, seq.LegalEntityID, seq.Code); err != nil {
			return err
		}
		next, err := takeNext(ctx, tx, p, seq)
		if err != nil {
			return err
		}
		formatted, err := domain.FormatValue(seq.Format, next)
		if err != nil {
			return err
		}
		var key *string
		if idempotencyKey != "" {
			key = &idempotencyKey
		}
		out = Reservation{ID: ids.New(), Sequence: seq.Code, LegalEntityID: seq.LegalEntityID, Value: next, Formatted: formatted, Status: domain.StatusReserved, LeaseExpiresAt: now.Add(lease)}
		_, err = tx.Exec(ctx, `insert into number_reservation
			(tenant_id, id, legal_entity_id, sequence_code, value, formatted, status, lease_expires_at, idempotency_key)
			values ($1,$2,$3,$4,$5,$6,$7,$8,$9)`, p.TenantID, out.ID, out.LegalEntityID, out.Sequence, out.Value, out.Formatted, out.Status, out.LeaseExpiresAt, key)
		return err
	})
	if err != nil {
		return Reservation{}, mapUniqueViolation(err)
	}
	return out, nil
}

// Confirm turns a live reservation into an issued number for documentRef.
func (s *Postgres) Confirm(ctx context.Context, id, documentRef string) (Reservation, error) {
	return s.settle(ctx, id, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, r Reservation, now time.Time) (Reservation, error) {
		if _, err := tx.Exec(ctx, `update number_reservation set status=$3, document_ref=$4, confirmed_at=$5 where tenant_id=$1 and id=$2`,
			p.TenantID, r.ID, domain.StatusConfirmed, documentRef, now); err != nil {
			return r, err
		}
		if _, err := tx.Exec(ctx, `insert into number_allocation (tenant_id, id, legal_entity_id, sequence_code, value, formatted)
			values ($1,$2,$3,$4,$5,$6)`, p.TenantID, ids.New(), r.LegalEntityID, r.Sequence, r.Value, r.Formatted); err != nil {
			return r, err
		}
		r.Status, r.DocumentRef = domain.StatusConfirmed, documentRef
		return r, enqueueReservationEvent(ctx, tx, p, confirmedType, r, map[string]any{"documentRef": documentRef})
	}, true)
}

// Void releases a live reservation and records the reason; the number is never offered again.
func (s *Postgres) Void(ctx context.Context, id, reason string) (Reservation, error) {
	return s.settle(ctx, id, func(ctx context.Context, tx pgx.Tx, p tenant.Principal, r Reservation, now time.Time) (Reservation, error) {
		if _, err := tx.Exec(ctx, `update number_reservation set status=$3, void_reason=$4, voided_at=$5 where tenant_id=$1 and id=$2`,
			p.TenantID, r.ID, domain.StatusVoided, reason, now); err != nil {
			return r, err
		}
		r.Status, r.VoidReason = domain.StatusVoided, reason
		return r, enqueueReservationEvent(ctx, tx, p, voidedType, r, map[string]any{"reason": reason})
	}, false)
}

type settleFunc func(ctx context.Context, tx pgx.Tx, p tenant.Principal, r Reservation, now time.Time) (Reservation, error)

func (s *Postgres) settle(ctx context.Context, id string, apply settleFunc, leaseDistinct bool) (Reservation, error) {
	p, _ := tenant.FromContext(ctx)
	now := s.clock()
	var out Reservation
	var refusal error
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if err := expireLeases(ctx, tx, p, now, forIDMatch, id); err != nil {
			return err
		}
		r, err := scanReservation(tx.QueryRow(ctx, `select `+reservationCols+` from number_reservation where tenant_id=$1 and id=$2 for update`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			refusal = httpx.NewProblem(http.StatusNotFound, domain.CodeReservationUnknown, "reservation not found")
			return nil
		}
		if err != nil {
			return err
		}
		if r.Status != domain.StatusReserved {
			refusal = httpx.NewProblem(http.StatusConflict, domain.CodeReservationState, "the reservation is already "+r.Status)
			if leaseDistinct && r.VoidReason == domain.VoidLeaseExpired {
				refusal = httpx.NewProblem(http.StatusConflict, domain.CodeLeaseExpired, "the lease expired; the number was voided and will not be reused")
			}
			return nil
		}
		out, err = apply(ctx, tx, p, r, now)
		return err
	})
	if err != nil {
		return Reservation{}, mapUniqueViolation(err)
	}
	if refusal != nil {
		return Reservation{}, refusal
	}
	return out, nil
}

// Voided lists the voided numbers of a sequence, expiring overdue leases first.
func (s *Postgres) Voided(ctx context.Context, code, after string, limit int) ([]VoidedNumber, error) {
	p, _ := tenant.FromContext(ctx)
	now := s.clock()
	items := []VoidedNumber{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		seq, err := selectSequence(ctx, tx, p, code, "")
		if err != nil {
			return err
		}
		if err := expireLeases(ctx, tx, p, now, forSequenceMatch, seq.LegalEntityID, seq.Code); err != nil {
			return err
		}
		rows, err := tx.Query(ctx, `select id, value, formatted, coalesce(void_reason,''), voided_at from number_reservation
			where tenant_id=$1 and legal_entity_id=$2 and sequence_code=$3 and status=$4 and id > $5
			order by id limit `+strconv.Itoa(limit), p.TenantID, seq.LegalEntityID, seq.Code, domain.StatusVoided, after)
		if err != nil {
			return err
		}
		items, err = pgx.CollectRows(rows, func(row pgx.CollectableRow) (VoidedNumber, error) {
			var v VoidedNumber
			err := row.Scan(&v.ID, &v.Value, &v.Formatted, &v.Reason, &v.VoidedAt)
			return v, err
		})
		return err
	})
	return items, err
}
