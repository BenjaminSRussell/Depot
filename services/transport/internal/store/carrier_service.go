package store

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/transport/internal/domain"
)

const carrierServiceColumns = `id, carrier_id, code, name, mode, active, request_hash`

// CarrierServiceFilter narrows the carrier-service list. Empty fields match everything.
type CarrierServiceFilter struct {
	CarrierID string
	// Q matches the code or name as typed (case-insensitive, any part); Code is the exact code.
	Q    string
	Code string
	// ActiveOnly hides retired services.
	ActiveOnly bool
	Limit      int
}

func scanCarrierService(row pgx.Row) (domain.CarrierService, string, error) {
	var c domain.CarrierService
	var hash *string
	err := row.Scan(&c.ID, &c.CarrierID, &c.Code, &c.Name, &c.Mode, &c.Active, &hash)
	return c, deref(hash), err
}

// CreateCarrierService saves a carrier service. A code already used by the carrier is a 409.
func (s *Postgres) CreateCarrierService(ctx context.Context, in domain.CarrierService, idem Idem) (domain.CarrierService, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.CarrierService
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		if idem.Key != "" {
			existing, hash, err := scanCarrierService(tx.QueryRow(ctx, `select `+carrierServiceColumns+` from carrier_service where tenant_id = $1 and idempotency_key = $2`, p.TenantID, idem.Key))
			if err == nil {
				if hash != idem.Fingerprint {
					return reuse()
				}
				out = existing
				return nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return err
			}
		}
		var err error
		out, _, err = scanCarrierService(tx.QueryRow(ctx, `insert into carrier_service (tenant_id, id, carrier_id, code, name, mode, active, idempotency_key, request_hash)
			values ($1, $2, $3, $4, $5, $6, true, nullif($7, ''), nullif($8, '')) returning `+carrierServiceColumns,
			p.TenantID, ids.New(), in.CarrierID, in.Code, strings.TrimSpace(in.Name), in.Mode, idem.Key, idem.Fingerprint))
		var pg *pgconn.PgError
		if errors.As(err, &pg) && pg.Code == uniqueViolation {
			return httpx.NewProblem(http.StatusConflict, domain.CodeCarrierServiceExists, "this carrier already has a service with that code")
		}
		return err
	})
	return out, err
}

// ListCarrierServices returns services ordered by carrier and code.
func (s *Postgres) ListCarrierServices(ctx context.Context, f CarrierServiceFilter) ([]domain.CarrierService, error) {
	p, _ := tenant.FromContext(ctx)
	out := []domain.CarrierService{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+carrierServiceColumns+` from carrier_service
			where tenant_id = $1 and ($2::text = '' or carrier_id::text = $2)
			and ($3::text = '' or strpos(lower(code), lower($3)) > 0 or strpos(lower(name), lower($3)) > 0)
			and ($4::text = '' or code = $4) and (not $5 or active)
			order by carrier_id, code limit $6`, p.TenantID, f.CarrierID, f.Q, f.Code, f.ActiveOnly, limitOr(f.Limit))
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			c, _, err := scanCarrierService(rows)
			if err != nil {
				return err
			}
			out = append(out, c)
		}
		return rows.Err()
	})
	return out, err
}

// GetCarrierService returns one service or a 404.
func (s *Postgres) GetCarrierService(ctx context.Context, id string) (domain.CarrierService, error) {
	p, _ := tenant.FromContext(ctx)
	var out domain.CarrierService
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, _, err = scanCarrierService(tx.QueryRow(ctx, `select `+carrierServiceColumns+` from carrier_service where tenant_id = $1 and id = $2`, p.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return NotFound("carrier service")
		}
		return err
	})
	return out, err
}
