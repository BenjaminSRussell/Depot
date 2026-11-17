package cdc

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/BenjaminSRussell/depot/platform/auditctx"
	"github.com/BenjaminSRussell/depot/platform/eventing"
	"github.com/BenjaminSRussell/depot/platform/events"
	"github.com/BenjaminSRussell/depot/platform/ids"
)

// The types the decoder gives CDC records. They are internal to the consumer, never published, and deliberately not
// CloudEvents types.
const (
	// TypeRowChange is an insert, update or delete of a registered table.
	TypeRowChange = "cdc:row-change"
	// TypeContext is a depot.ctx logical-decoding message.
	TypeContext = "cdc:context"
	// TypeIgnored is anything else the connector writes (snapshot reads, truncates, other message prefixes, a
	// malformed depot.ctx message): the consumer skips it.
	TypeIgnored = "cdc:ignored"
)

// unavailable is the value Debezium writes for an unchanged TOASTed column it does not have.
const unavailable = "__debezium_unavailable_value"

// ErrUnregistered is a change of a table the registry does not know: the record is dead-lettered so that it can be
// replayed once the table is registered.
var ErrUnregistered = errors.New("cdc: change of an unregistered table")

// envelope is a Debezium PostgreSQL change or message event, JSON with schemas disabled (or the payload of one with
// schemas enabled).
type envelope struct {
	Before  map[string]json.RawMessage `json:"before"`
	After   map[string]json.RawMessage `json:"after"`
	Source  source                     `json:"source"`
	Op      string                     `json:"op"`
	TsMs    int64                      `json:"ts_ms"`
	Message *struct {
		Prefix  string `json:"prefix"`
		Content []byte `json:"content"`
	} `json:"message"`
}

type source struct {
	Name   string `json:"name"`
	Schema string `json:"schema"`
	Table  string `json:"table"`
	TxID   *int64 `json:"txId"`
	LSN    *int64 `json:"lsn"`
	TsMs   int64  `json:"ts_ms"`
}

func (s source) position() (int64, int64, bool) {
	if s.TxID == nil || s.LSN == nil {
		return 0, 0, false
	}
	return *s.TxID, *s.LSN, true
}

func parse(value []byte) (envelope, error) {
	var env envelope
	var wrapped struct {
		Schema  json.RawMessage `json:"schema"`
		Payload json.RawMessage `json:"payload"`
	}
	if err := json.Unmarshal(value, &wrapped); err == nil && len(wrapped.Schema) > 0 && len(wrapped.Payload) > 0 {
		value = wrapped.Payload
	}
	if err := json.Unmarshal(value, &env); err != nil {
		return envelope{}, fmt.Errorf("cdc: not a Debezium event: %w", err)
	}
	return env, nil
}

func (e envelope) occurredAt() time.Time {
	ms := e.Source.TsMs
	if ms == 0 {
		ms = e.TsMs
	}
	return time.UnixMilli(ms).UTC()
}

// Decoder turns the records of the audit CDC topics into the events the consumer's handlers see
// (eventing.Config.Decode). Each event's ID is its CDC position, so the inbox makes the consumer idempotent by it.
type Decoder struct{ Registry Registry }

// Decode implements eventing.Config.Decode.
func (d Decoder) Decode(r eventing.Record) (events.Event, error) {
	ignored := events.Event{Type: TypeIgnored, ID: fmt.Sprintf("%s:%d:%d", r.Topic, r.Partition, r.Offset)}
	if len(bytes.TrimSpace(r.Value)) == 0 || bytes.Equal(bytes.TrimSpace(r.Value), []byte("null")) {
		return ignored, nil
	}
	env, err := parse(r.Value)
	if err != nil {
		return events.Event{}, err
	}
	txID, lsn, positioned := env.Source.position()
	switch env.Op {
	case "m":
		src, known := d.Registry.Source(env.Source.Name)
		if env.Message == nil || env.Message.Prefix != auditctx.Prefix || !positioned || !known {
			return ignored, nil
		}
		msg, err := auditctx.Decode(env.Message.Content)
		if err != nil {
			return ignored, nil
		}
		return events.Event{
			ID: fmt.Sprintf("%s:%d:%d:ctx", src, txID, lsn), Type: TypeContext, TenantID: msg.TenantID,
			Time: env.occurredAt(), Data: r.Value,
		}, nil
	case "c", "u", "d":
		table, ok := d.Registry.Lookup(env.Source.Name, env.Source.Schema+"."+env.Source.Table)
		if !ok {
			return events.Event{}, fmt.Errorf("%w: %s %s.%s", ErrUnregistered, env.Source.Name, env.Source.Schema, env.Source.Table)
		}
		if !positioned {
			return events.Event{}, errors.New("cdc: change event without txId and lsn")
		}
		row := env.After
		if env.Op == "d" {
			row = env.Before
		}
		tenantID, _ := text(row[tenantColumn])
		if !ids.Valid(tenantID) {
			return events.Event{}, fmt.Errorf("cdc: %s row without a tenant_id UUID", table.Table)
		}
		recordID, err := recordID(table, row)
		if err != nil {
			return events.Event{}, err
		}
		return events.Event{
			ID:   fmt.Sprintf("%s:%d:%d:%s:%s", table.Source, txID, lsn, table.Table, recordID),
			Type: TypeRowChange, TenantID: tenantID, Time: env.occurredAt(), Subject: recordID, Data: r.Value,
		}, nil
	default:
		// "r" (snapshot read; the connectors use snapshot.mode no_data) and "t" (truncate; the publications do not
		// publish it).
		return ignored, nil
	}
}

func recordID(t Table, row map[string]json.RawMessage) (string, error) {
	parts := make([]string, 0, len(t.Key))
	for _, k := range t.Key {
		v, ok := text(row[k])
		if !ok || v == "" {
			return "", fmt.Errorf("cdc: %s row without key column %s", t.Table, k)
		}
		parts = append(parts, v)
	}
	return strings.Join(parts, "/"), nil
}

// text renders a Debezium JSON value as audit text: a string as is, null as "", any other value as its JSON text.
// It reports false for a value Debezium does not have (an unchanged TOASTed column).
func text(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if len(raw) == 0 || bytes.Equal(raw, []byte("null")) {
		return "", true
	}
	if raw[0] == '"' {
		var s string
		if err := json.Unmarshal(raw, &s); err != nil {
			return "", false
		}
		return s, s != unavailable
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, raw); err != nil {
		return "", false
	}
	return compact.String(), true
}

func render(raw json.RawMessage, f Field) (string, bool) {
	v, ok := text(raw)
	if !ok || v == "" || f.Format != FormatDate {
		return v, ok
	}
	var days int64
	if _, err := fmt.Sscan(v, &days); err != nil {
		return v, true
	}
	return time.Unix(days*86400, 0).UTC().Format(time.DateOnly), true
}
