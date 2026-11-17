package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/dbx"
)

// VendorProfile says the partner is one of our vendors: its inbound 810 is a vendor invoice for this vendor in this legal entity.
type VendorProfile struct {
	LegalEntityID string `json:"legalEntityId"`
	VendorID      string `json:"vendorId"`
}

// PutVendorProfile stores the partner's vendor profile. ErrNotFound when the partner does not exist.
func (s *Store) PutVendorProfile(ctx context.Context, partnerID string, p VendorProfile) error {
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		tid, err := tenantID(ctx)
		if err != nil {
			return err
		}
		var exists bool
		if err := tx.QueryRow(ctx, `select exists(select 1 from trading_partner where id = $1)`, partnerID).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrNotFound
		}
		_, err = tx.Exec(ctx, `insert into partner_vendor_profile (tenant_id, partner_id, legal_entity_id, vendor_id) values ($1, $2, $3, $4)
			on conflict (tenant_id, partner_id) do update set legal_entity_id = excluded.legal_entity_id, vendor_id = excluded.vendor_id, updated_at = now()`,
			tid, partnerID, p.LegalEntityID, p.VendorID)
		return err
	})
}

// GetVendorProfile returns the partner's vendor profile, or ErrNotFound.
func (s *Store) GetVendorProfile(ctx context.Context, partnerID string) (VendorProfile, error) {
	var p VendorProfile
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		err := tx.QueryRow(ctx, `select legal_entity_id::text, vendor_id::text from partner_vendor_profile where partner_id = $1`, partnerID).Scan(&p.LegalEntityID, &p.VendorID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrNotFound
		}
		return err
	})
	return p, err
}

// HasSelfBillConfig reports whether the partner self-bills (it has self-billing settings). A partner is a self-billing customer or one of
// our vendors for its 810s, never both.
func (s *Store) HasSelfBillConfig(ctx context.Context, partnerID string) (bool, error) {
	var has bool
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		return tx.QueryRow(ctx, `select exists(select 1 from self_billing_config where partner_id = $1)`, partnerID).Scan(&has)
	})
	return has, err
}
