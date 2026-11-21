package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/dbx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/domain"
)

const linkColumns = `id::text, pair_id, source_entity, source_type, source_id, mirror_entity, mirror_type, mirror_id, pricing_basis, status, created_by, created_at`

func scanLink(row pgx.Row) (domain.DocumentLink, error) {
	var l domain.DocumentLink
	err := row.Scan(&l.ID, &l.PairID, &l.SourceEntity, &l.SourceType, &l.SourceID, &l.MirrorEntity, &l.MirrorType, &l.MirrorID, &l.PricingBasis, &l.Status, &l.CreatedBy, &l.CreatedAt)
	return l, err
}

func (s *Postgres) CreateLink(ctx context.Context, l domain.DocumentLink) (domain.DocumentLink, bool, error) {
	if err := l.Validate(); err != nil {
		return domain.DocumentLink{}, false, err
	}
	pr, by, err := principalOf(ctx)
	if err != nil {
		return domain.DocumentLink{}, false, err
	}
	var out domain.DocumentLink
	created := false
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanLink(tx.QueryRow(ctx, `insert into ic_document_link (tenant_id, id, pair_id, source_entity, source_type, source_id, mirror_entity, mirror_type, mirror_id, pricing_basis, created_by)
			values ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
			on conflict (tenant_id, source_type, source_id) do nothing returning `+linkColumns,
			pr.TenantID, ids.New(), l.PairID, l.SourceEntity, l.SourceType, l.SourceID, l.MirrorEntity, l.MirrorType, l.MirrorID, l.PricingBasis, by))
		if err == nil {
			created = true
			return nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		out, err = scanLink(tx.QueryRow(ctx, `select `+linkColumns+` from ic_document_link where tenant_id = $1 and source_type = $2 and source_id = $3`, pr.TenantID, l.SourceType, l.SourceID))
		if err != nil {
			return err
		}
		if !out.SameDocuments(l) {
			return &domain.Error{Code: domain.CodeLinkConflict, Detail: "the " + l.SourceType + " " + l.SourceID + " is already linked to " + out.MirrorType + " " + out.MirrorID}
		}
		return nil
	})
	if err != nil {
		return domain.DocumentLink{}, false, wrap("create intercompany document link", err)
	}
	return out, created, nil
}

func (s *Postgres) ListLinks(ctx context.Context, pairID string) ([]domain.DocumentLink, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return nil, err
	}
	out := []domain.DocumentLink{}
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		rows, err := tx.Query(ctx, `select `+linkColumns+` from ic_document_link where tenant_id = $1 and ($2::text = '' or pair_id = $2) order by created_at desc, id limit 500`, pr.TenantID, pairID)
		if err != nil {
			return err
		}
		defer rows.Close()
		for rows.Next() {
			l, err := scanLink(rows)
			if err != nil {
				return err
			}
			out = append(out, l)
		}
		return rows.Err()
	})
	if err != nil {
		return nil, wrap("list intercompany document links", err)
	}
	return out, nil
}

func (s *Postgres) CompensateLink(ctx context.Context, id string) (domain.DocumentLink, error) {
	pr, _, err := principalOf(ctx)
	if err != nil {
		return domain.DocumentLink{}, err
	}
	var out domain.DocumentLink
	err = dbx.WithTenantTx(ctx, s.pool, func(tx pgx.Tx) error {
		var err error
		out, err = scanLink(tx.QueryRow(ctx, `update ic_document_link set status = 'compensated' where tenant_id = $1 and id = $2 returning `+linkColumns, pr.TenantID, id))
		if errors.Is(err, pgx.ErrNoRows) {
			return &domain.Error{Code: domain.CodeLinkNotFound, Detail: "no intercompany document link with id " + id}
		}
		return err
	})
	if err != nil {
		return domain.DocumentLink{}, wrap("compensate intercompany document link", err)
	}
	return out, nil
}
