// Package outbox writes events into the service's outbox table inside the
// caller's transaction. Debezium's outbox event router relays rows to Kafka,
// so a business change and its event are committed atomically or not at all.
package outbox

import (
	"context"
	"encoding/json"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/tracectx"
)

var lastSequence atomic.Int64

func nextSequence() int64 {
	for {
		last := lastSequence.Load()
		next := max(time.Now().UnixMicro(), last+1)
		if lastSequence.CompareAndSwap(last, next) {
			return next
		}
	}
}

// Enqueue stores evt in the outbox within tx. aggregateType/aggregateID become
// the Kafka key so events for one aggregate stay ordered. It stamps the trace
// context of ctx, a sequence and the data schema onto an event that lacks them.
func Enqueue(ctx context.Context, tx pgx.Tx, aggregateType, aggregateID string, evt events.Event) error {
	if span, ok := tracectx.From(ctx); ok && evt.Traceparent == "" {
		evt.Traceparent = span.Traceparent()
	}
	if evt.Sequence <= 0 {
		evt.Sequence = nextSequence()
	}
	if evt.DataSchema == "" {
		evt.DataSchema = events.DataSchema(evt.Type)
	}
	payload, err := json.Marshal(evt)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `insert into outbox(id, tenant_id, aggregate_type, aggregate_id, type, payload) values($1,$2,$3,$4,$5,$6)`,
		evt.ID, evt.TenantID, aggregateType, aggregateID, evt.Type, payload)
	return err
}
