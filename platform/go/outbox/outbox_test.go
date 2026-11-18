package outbox_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/outbox"
	"github.com/BenjaminSRussell/depot/platform/tracectx"
)

const eventType = "dev.depot.receiving.receipt.posted.v1"

type fakeTx struct {
	pgx.Tx
	payloads [][]byte
	err      error
}

func (f *fakeTx) Exec(_ context.Context, _ string, args ...any) (pgconn.CommandTag, error) {
	if f.err != nil {
		return pgconn.CommandTag{}, f.err
	}
	f.payloads = append(f.payloads, args[5].([]byte))
	return pgconn.CommandTag{}, nil
}

func newEvent(t *testing.T) events.Event {
	t.Helper()
	evt, err := events.New(eventType, "/depot/receiving", "r1", "11111111-1111-4111-8111-111111111111", map[string]string{"id": "r1"})
	if err != nil {
		t.Fatal(err)
	}
	return evt
}

func stored(t *testing.T, tx *fakeTx, i int) events.Event {
	t.Helper()
	var evt events.Event
	if err := json.Unmarshal(tx.payloads[i], &evt); err != nil {
		t.Fatal(err)
	}
	return evt
}

func TestEnqueueStampsTraceparentSequenceAndDataSchema(t *testing.T) {
	root, _ := tracectx.Parse("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	tx := &fakeTx{}
	if err := outbox.Enqueue(tracectx.With(context.Background(), root), tx, "receipt", "r1", newEvent(t)); err != nil {
		t.Fatal(err)
	}
	got := stored(t, tx, 0)
	span, ok := tracectx.Parse(got.Traceparent)
	if !ok || span.TraceID != root.TraceID || got.Sequence < 1 || got.DataSchema != "https://schemas.depot.dev/"+eventType+".json" {
		t.Fatalf("stored %+v", got)
	}
}

func TestEnqueueWithoutASpanLeavesTraceparentOut(t *testing.T) {
	tx := &fakeTx{}
	if err := outbox.Enqueue(context.Background(), tx, "receipt", "r1", newEvent(t)); err != nil {
		t.Fatal(err)
	}
	got := stored(t, tx, 0)
	if got.Traceparent != "" || got.Sequence < 1 || got.DataSchema == "" {
		t.Fatalf("stored %+v", got)
	}
}

func TestEnqueueKeepsValuesTheCallerSet(t *testing.T) {
	root, _ := tracectx.Parse("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")
	evt := newEvent(t)
	evt.Traceparent, evt.Sequence, evt.DataSchema = "00-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa-bbbbbbbbbbbbbbbb-01", 7, "https://schemas.example/x.json"
	tx := &fakeTx{}
	if err := outbox.Enqueue(tracectx.With(context.Background(), root), tx, "receipt", "r1", evt); err != nil {
		t.Fatal(err)
	}
	if got := stored(t, tx, 0); got.Traceparent != evt.Traceparent || got.Sequence != 7 || got.DataSchema != evt.DataSchema {
		t.Fatalf("stored %+v", got)
	}
}

func TestEnqueueReplacesANegativeSequenceAndKeepsGeneratedOnesIncreasing(t *testing.T) {
	evt := newEvent(t)
	evt.Sequence = -3
	tx := &fakeTx{}
	for range 2 {
		if err := outbox.Enqueue(context.Background(), tx, "receipt", "r1", evt); err != nil {
			t.Fatal(err)
		}
	}
	first, second := stored(t, tx, 0), stored(t, tx, 1)
	if first.Sequence < 1 || second.Sequence <= first.Sequence {
		t.Fatalf("sequences %d then %d", first.Sequence, second.Sequence)
	}
}

func TestEnqueueReturnsTheExecError(t *testing.T) {
	boom := errors.New("connection reset")
	if err := outbox.Enqueue(context.Background(), &fakeTx{err: boom}, "receipt", "r1", newEvent(t)); !errors.Is(err, boom) {
		t.Fatalf("err = %v", err)
	}
}
