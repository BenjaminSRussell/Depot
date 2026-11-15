package store

import (
	"context"
	"errors"
	"net/http"
	"regexp"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// CodeTaxRegistrationInvalid is PTY-TAXREG-001: the country or VAT id is not valid.
const CodeTaxRegistrationInvalid = "PTY-TAXREG-001"

var (
	countryPattern = regexp.MustCompile(`^[A-Z]{2}$`)
	vatIDPattern   = regexp.MustCompile(`^[A-Z0-9]{0,20}$`)
)

// TaxRegistration is the country a customer or vendor role's VAT registration is in, and its VAT id.
type TaxRegistration struct {
	PartnerID string `json:"partnerId"`
	Kind      string `json:"kind"`
	Country   string `json:"country"`
	VATID     string `json:"vatId"`
}

// TaxRegistrationStore keeps the tax registration of customer and vendor roles.
type TaxRegistrationStore interface {
	// SetTaxRegistration sets the registration of the customer or vendor role partnerID in the caller's legal entities.
	SetTaxRegistration(ctx context.Context, partnerID, country, vatID string) (TaxRegistration, error)
	// TaxRegistrations reads the registrations of the named roles; a role without one is left out.
	TaxRegistrations(ctx context.Context, partnerIDs []string) ([]TaxRegistration, error)
}

// SetTaxRegistration upserts the registration of a role; the role must be a customer or vendor role the caller's legal-entity scope
// reaches (row-level security), else 404.
func (s *Postgres) SetTaxRegistration(ctx context.Context, partnerID, country, vatID string) (TaxRegistration, error) {
	caller, _ := tenant.FromContext(ctx)
	id, err := canonicalID(partnerID)
	if err != nil {
		return TaxRegistration{}, ErrNotFound
	}
	if !countryPattern.MatchString(country) || !vatIDPattern.MatchString(vatID) {
		return TaxRegistration{}, httpx.NewProblem(http.StatusUnprocessableEntity, CodeTaxRegistrationInvalid,
			"country must be a 2-letter ISO 3166 code in capitals and the VAT id at most 20 letters and digits")
	}
	out := TaxRegistration{PartnerID: id, Country: country, VATID: vatID}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var legalEntity string
		err := tx.QueryRow(ctx, `select legal_entity_id::text, 'CUSTOMER' from customer_role where tenant_id = $1 and id = $2
			union all select legal_entity_id::text, 'VENDOR' from vendor_role where tenant_id = $1 and id = $2`, caller.TenantID, id).Scan(&legalEntity, &out.Kind)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		if err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `insert into partner_tax_registration (tenant_id, partner_id, legal_entity_id, partner_kind, country, vat_id, updated_by)
			values ($1, $2, $3, $4, $5, $6, $7)
			on conflict (tenant_id, partner_id) do update set country = excluded.country, vat_id = excluded.vat_id, updated_by = excluded.updated_by, updated_at = now()`,
			caller.TenantID, id, legalEntity, out.Kind, country, vatID, caller.UserID)
		return err
	})
	return out, err
}

// TaxRegistrations reads the registrations of up to 200 roles in one call.
func (s *Postgres) TaxRegistrations(ctx context.Context, partnerIDs []string) ([]TaxRegistration, error) {
	caller, _ := tenant.FromContext(ctx)
	wanted := make([]string, 0, len(partnerIDs))
	for _, p := range partnerIDs {
		id, err := canonicalID(p)
		if err != nil {
			return nil, ErrInvalidID
		}
		wanted = append(wanted, id)
	}
	out := []TaxRegistration{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select partner_id::text, partner_kind, country::text, vat_id from partner_tax_registration
			where tenant_id = $1 and partner_id::text = any($2::text[]) order by partner_id`, caller.TenantID, wanted)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var r TaxRegistration
			if err := rows.Scan(&r.PartnerID, &r.Kind, &r.Country, &r.VATID); err != nil {
				return err
			}
			out = append(out, r)
		}
		return rows.Err()
	})
	return out, err
}
