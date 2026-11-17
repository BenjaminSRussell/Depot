package store

import (
	"context"
	"errors"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/tenant"
)

// widenForSystemCaller sets tx's legal-entity scope to every legal entity of the tenant when the caller holds no
// legal-entity claim and is a system actor: the shop floor sweeper (system:shopfloor-sweeper) and service principals
// (service:<name>), which keep the tenant's time data consistent whatever legal entity employs the worker (B-03b, D17):
// the auto clock-out of stale shifts and the stop of job starts on an operation production finished. A caller with a
// claim keeps its own scope, and a person without one keeps the empty scope dbx gave it.
func widenForSystemCaller(ctx context.Context, tx pgx.Tx) error {
	p, ok := tenant.FromContext(ctx)
	if !ok {
		return errors.New("tenant context is required")
	}
	if p.LegalEntityID != "" || !systemActor(p.UserID) {
		return nil
	}
	_, err := tx.Exec(ctx, `select set_config('depot.legal_entity_scopes', '*', true)`)
	return err
}

func systemActor(userID string) bool {
	return tenant.IsServiceActor(userID) || userID == SweepActor
}

// SweepActor is the actor the shop floor sweeper runs as.
const SweepActor = "system:shopfloor-sweeper"
