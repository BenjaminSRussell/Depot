package store

import (
	"context"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/passage/platform/dbx"
)

// OpenItemPartners maps the open item id of each line of a payment proposal that carries an intercompany trading partner to it (#40);
// items posted without one are absent, so their payment lines stay untagged.
func (s *Postgres) OpenItemPartners(ctx context.Context, proposalID string) (map[string]string, error) {
	out := map[string]string{}
	err := dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select l.open_item_id::text, i.trading_partner_id::text from ap_payment_proposal_line l
			join ap_open_item i on i.tenant_id = l.tenant_id and i.id = l.open_item_id
			where l.proposal_id = $1::uuid and i.trading_partner_id is not null`, proposalID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			var item, partner string
			if err := rows.Scan(&item, &partner); err != nil {
				return err
			}
			out[item] = partner
		}
		return rows.Err()
	})
	return out, err
}
