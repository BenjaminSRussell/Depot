package cdc_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/auditctx"
	"github.com/BenjaminSRussell/depot/platform/eventing"
)

// The fixtures are what the audit-cdc-party connector writes (deploy/kafka/connect.yaml): Debezium 3 PostgreSQL events
// from pgoutput, JSON without schemas, change events and depot.ctx messages routed to one topic. Bytes (a message's
// content) are base64, as JsonConverter writes them.

const (
	topic  = "depot.cdc.party"
	server = "depot.cdc.party"
	le1    = "2f6c3e0a-8d6b-4c1e-9a43-0c5f3b1d7e21"
	le2    = "7a1d9e44-3b2c-4f5a-8e61-9c0b2d3e4f50"
)

var commit = time.Date(2026, 11, 3, 9, 15, 0, 0, time.UTC)

func src(txID, lsn int64, table string) map[string]any {
	s := map[string]any{"version": "3.0.7.Final", "connector": "postgresql", "name": server, "ts_ms": commit.UnixMilli(),
		"snapshot": "false", "db": "depot_party", "sequence": `["100","200"]`, "schema": "public", "table": table, "txId": txID, "lsn": lsn, "xmin": nil}
	if table == "" {
		s["schema"] = ""
	}
	return s
}

func value(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

var offset int64

func rec(t *testing.T, key, val any) eventing.Record {
	t.Helper()
	offset++
	return eventing.Record{Topic: topic, Partition: 0, Offset: offset, Key: value(t, key), Value: value(t, val)}
}

// ctxRecord is the depot.ctx message of transaction txID at position lsn.
func ctxRecord(t *testing.T, txID, lsn int64, m auditctx.Message) eventing.Record {
	t.Helper()
	return rec(t, map[string]any{"prefix": auditctx.Prefix}, map[string]any{
		"op": "m", "ts_ms": commit.UnixMilli(), "source": src(txID, lsn, ""),
		"message": map[string]any{"prefix": auditctx.Prefix, "content": value(t, m)},
	})
}

// account is a vendor_bank_account row; the sealed columns are excluded by the connector.
func account(tenantID, id, le, holder, last4 string) map[string]any {
	return map[string]any{"tenant_id": tenantID, "id": id, "vendor_id": "9d1e2f3a-4b5c-4d6e-8f70-1a2b3c4d5e6f", "legal_entity_id": le,
		"account_holder": holder, "bank_country": "DE", "currency_code": "EUR", "account_last4": last4,
		"created_by": "ana", "created_at": "2026-11-03T09:15:00.000000Z"}
}

func changeRecord(t *testing.T, op string, txID, lsn int64, before, after map[string]any) eventing.Record {
	t.Helper()
	row := after
	if row == nil {
		row = before
	}
	return rec(t, map[string]any{"tenant_id": row["tenant_id"], "id": row["id"]}, map[string]any{
		"before": before, "after": after, "source": src(txID, lsn, "vendor_bank_account"), "op": op,
		"ts_ms": commit.Add(time.Second).UnixMilli(), "transaction": nil,
	})
}
