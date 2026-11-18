package store

import (
	"reflect"
	"testing"
	"time"
)

func TestRecordWhereNumbersArgumentsAfterTheTenant(t *testing.T) {
	from := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clause, args := recordWhere(RecordFilter{Entity: "e", UserID: "u", Field: "f", From: &from, AfterSeq: 9})
	wantClause := " and entity = $2 and user_id = $3 and field = $4 and occurred_at >= $5 and seq > $6"
	if clause != wantClause || !reflect.DeepEqual(args, []any{"e", "u", "f", from, int64(9)}) {
		t.Fatalf("got %q %v", clause, args)
	}
	if clause, args := recordWhere(RecordFilter{}); clause != "" || len(args) != 0 {
		t.Fatalf("empty filter gave %q %v", clause, args)
	}
}
