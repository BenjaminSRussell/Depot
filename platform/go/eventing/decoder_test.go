package eventing_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/events"
)

// A consumer of records that are not CloudEvents (the audit CDC consumer) supplies Config.Decode: its events reach the
// handlers with the ID it gives (the inbox key), and its errors dead-letter the record as an undecodable one.
func TestConsumerUsesTheConfiguredDecoder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	src := &fakeSource{cancel: cancel}
	dlq := &fakeDLQ{}
	inbox := &fakeInbox{seen: map[string]bool{}}
	var ran []events.Event
	c, err := eventing.NewConsumer(src, dlq, inbox, eventing.Config{
		Name: "receiving-ers", DeadLetterTopic: "depot.cdc.dlq", Backoff: func(int) time.Duration { return 0 },
		Decode: func(r eventing.Record) (events.Event, error) {
			if string(r.Value) == "bad" {
				return events.Event{}, errors.New("not a change event")
			}
			return events.Event{ID: "pos-" + string(r.Value), Type: "cdc:row", TenantID: tenantA, Data: r.Value}, nil
		},
		Handlers: map[string]eventing.Handler{"cdc:row": eventing.HandlerFunc(func(_ context.Context, _ pgx.Tx, evt events.Event) error {
			ran = append(ran, evt)
			return nil
		})},
	})
	if err != nil {
		t.Fatal(err)
	}
	src.queue = []eventing.Record{{Offset: 1, Value: []byte("7")}, {Offset: 2, Value: []byte("7")}, {Offset: 3, Value: []byte("bad")}}
	if err := run(t, c, ctx); err != nil {
		t.Fatal(err)
	}
	if len(ran) != 1 || ran[0].ID != "pos-7" || !inbox.seen["receiving-ers/pos-7"] {
		t.Fatalf("ran %+v, inbox %v", ran, inbox.seen)
	}
	if len(dlq.sent) != 1 || dlq.sent[0].offset != 3 || len(src.committed) != 3 {
		t.Fatalf("dead-lettered %+v, committed %v", dlq.sent, src.committed)
	}
}
