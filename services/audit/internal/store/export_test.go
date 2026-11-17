package store

import (
	"context"

	"github.com/jackc/pgx/v5"
)

// BreakGlassSelect exposes the queue query to the plan test.
const BreakGlassSelect = breakGlassSelect

// VerifyChangesLimit walks with a page size of limit rows, so a test can reach a page boundary.
func VerifyChangesLimit(s *Postgres, ctx context.Context, fromSeq, toSeq, limit int64) (ChangeChain, error) {
	var out ChangeChain
	err := s.chainTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, err = verifyChanges(ctx, tx, fromSeq, toSeq, limit)
		return err
	})
	return out, err
}
