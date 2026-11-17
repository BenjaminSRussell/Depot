package store

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/ids"
)

// blockingGate holds a dry run inside its transaction: the close service answers only when released.
type blockingGate struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *blockingGate) Status(context.Context, string, string, time.Time, string) (string, error) {
	g.once.Do(func() { close(g.entered) })
	<-g.release
	return statusOpen, nil
}

// Validate and batch submit write nothing, so they read the lock without taking any row lock: a validation
// waiting on the close service inside its transaction blocks neither a posting nor close's lock update.
func TestPostgres_ValidationInFlightBlocksNeitherAPostingNorTheLockUpdate(t *testing.T) {
	e, _ := setup(t)
	gate := &blockingGate{entered: make(chan struct{}), release: make(chan struct{})}
	validated := make(chan error, 1)
	go func() {
		_, err := e.store.ValidateEntry(WithPeriodGate(e.ctx, gate), vendorInvoice(e.ledger.ID, "DRY-1", "10.00"))
		validated <- err
	}()
	<-gate.entered
	done := make(chan error, 1)
	go func() {
		if _, err := e.store.PostEntry(openGate(e.ctx), vendorInvoice(e.ledger.ID, "WHILE-DRY-1", "10.00")); err != nil {
			done <- err
			return
		}
		_, err := e.store.PutPeriodLock(e.ctx, lockInput(e, ids.New(), 1, statusClosed))
		done <- err
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("posting or lock update while a validation was in flight: %v", err)
		}
	case <-time.After(5 * time.Second):
		close(gate.release)
		t.Fatal("a validation waiting on the close service blocked a posting or the lock update")
	}
	select {
	case err := <-validated:
		t.Fatalf("the validation returned before it was released: %v", err)
	default:
	}
	close(gate.release)
	if err := <-validated; err != nil {
		t.Fatalf("validation: %v", err)
	}
}
