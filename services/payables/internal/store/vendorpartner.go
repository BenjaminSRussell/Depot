package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
	"github.com/BenjaminSRussell/passage/platform/tenant"
)

// RecordVendorPartner remembers that vendorID of the legal entity is an intercompany vendor whose other side is partnerLegalEntityID (#40).
// The intercompany charge consumer records it when it takes a charge in; the payment run names it on the vendor's control debit.
func (s *Postgres) RecordVendorPartner(ctx context.Context, legalEntityID, vendorID, partnerLegalEntityID string) error {
	p, _ := tenant.FromContext(ctx)
	return dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `insert into ap_vendor_partner (tenant_id, legal_entity_id, vendor_id, partner_legal_entity_id)
			values ($1, $2::uuid, $3::uuid, $4::uuid)
			on conflict (tenant_id, legal_entity_id, vendor_id) do update set partner_legal_entity_id = excluded.partner_legal_entity_id, updated_at = now()`,
			p.TenantID, legalEntityID, vendorID, partnerLegalEntityID)
		return err
	})
}

// VendorPartners maps the vendor id of each group of a payment proposal to its intercompany partner; ordinary vendors are absent (#40).
func (s *Postgres) VendorPartners(ctx context.Context, proposalID string) (map[string]string, error) {
	out := map[string]string{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select distinct i.vendor_id::text, vp.partner_legal_entity_id::text from ap_payment_proposal_line l
			join ap_open_item i on i.tenant_id = l.tenant_id and i.id = l.open_item_id
			join ap_vendor_partner vp on vp.tenant_id = i.tenant_id and vp.legal_entity_id = i.legal_entity_id and vp.vendor_id = i.vendor_id
			where l.proposal_id = $1::uuid`, proposalID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var vendor, partner string
			if err := rows.Scan(&vendor, &partner); err != nil {
				return err
			}
			out[vendor] = partner
		}
		return rows.Err()
	})
	return out, err
}
