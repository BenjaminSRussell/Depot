package cdc_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/auditctx"
	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/tenant"
)

// The fixtures are what the audit-cdc-party connector writes (deploy/kafka/connect.yaml): Debezium 3 PostgreSQL events
// from pgoutput, JSON without schemas, change events and depot.ctx messages routed to one topic. Bytes (a message's
// content) are base64, as JsonConverter writes them. A depot.ctx message is the signed envelope auditctx.Emit writes.

const (
	topic  = "depot.cdc.party"
	server = "depot.cdc.party"
	le1    = "2f6c3e0a-8d6b-4c1e-9a43-0c5f3b1d7e21"
	le2    = "7a1d9e44-3b2c-4f5a-8e61-9c0b2d3e4f50"
	// epoch is the transaction id epoch txid_current() carries above the 32-bit xid Debezium reports.
	epoch = int64(1) << 32
)

var commit = time.Date(2026, 11, 3, 9, 15, 0, 0, time.UTC)

// partyKey signs as party, the source service of the captured tables; partyPub is what audit trusts for it.
var partyKey, partyPub = func() (*tenant.ServiceSigner, ed25519.PublicKey) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		panic(err)
	}
	s, err := tenant.NewServiceSigner("party", priv)
	if err != nil {
		panic(err)
	}
	return s.WithKeyID("2026-11"), pub
}()

// trusted is the audit consumer's key registry: party's key under its kid.
var trusted = tenant.NewServiceVerifierKids("audit", map[string]map[string]ed25519.PublicKey{"party": {"2026-11": partyPub}})

func src(txID, lsn int64, table string, at time.Time) map[string]any {
	s := map[string]any{"version": "3.0.7.Final", "connector": "postgresql", "name": server, "ts_ms": at.UnixMilli(),
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

// ctxRecord is the depot.ctx message of transaction txID at position lsn, signed by party in that transaction.
func ctxRecord(t *testing.T, txID, lsn int64, m auditctx.Message) eventing.Record {
	t.Helper()
	return ctxRecordWith(t, txID, lsn, m, partyKey, strconv.FormatInt(epoch+txID, 10), commit)
}

// ctxRecordWith is a depot.ctx message Debezium saw in transaction txID at position lsn, committed at at, sealed by
// signer (nil: unsigned) as if emitted in transaction signedTxID.
func ctxRecordWith(t *testing.T, txID, lsn int64, m auditctx.Message, signer *tenant.ServiceSigner, signedTxID string, at time.Time) eventing.Record {
	t.Helper()
	content, err := auditctx.Seal(signer, m, signedTxID)
	if err != nil {
		t.Fatal(err)
	}
	return rec(t, map[string]any{"prefix": auditctx.Prefix}, map[string]any{
		"op": "m", "ts_ms": at.UnixMilli(), "source": src(txID, lsn, "", at),
		"message": map[string]any{"prefix": auditctx.Prefix, "content": content},
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
	return changeRecordAt(t, op, txID, lsn, before, after, commit)
}

func changeRecordAt(t *testing.T, op string, txID, lsn int64, before, after map[string]any, at time.Time) eventing.Record {
	t.Helper()
	row := after
	if row == nil {
		row = before
	}
	return rec(t, map[string]any{"tenant_id": row["tenant_id"], "id": row["id"]}, map[string]any{
		"before": before, "after": after, "source": src(txID, lsn, "vendor_bank_account", at), "op": op,
		"ts_ms": at.Add(time.Second).UnixMilli(), "transaction": nil,
	})
}
