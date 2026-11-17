package events

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestTypeAndTopic(t *testing.T) {
	typ := Type("ledger", "journal", "posted", 1)
	if typ != "dev.depot.ledger.journal.posted.v1" {
		t.Fatalf("got %s", typ)
	}
	if !ValidType(typ) {
		t.Fatal("expected valid")
	}
	if Topic(typ) != "depot.ledger.journal" {
		t.Fatalf("topic %s", Topic(typ))
	}
	for _, bad := range []string{"ledger.journal.posted", "dev.depot.Ledger.journal.posted.v1", "dev.depot.ledger.journal.posted"} {
		if ValidType(bad) {
			t.Errorf("%s should be invalid", bad)
		}
	}
}

func TestNew(t *testing.T) {
	e, err := New(Type("party", "customer", "created", 1), "/services/party", "cust-1", "tenant-a", map[string]string{"id": "cust-1"})
	if err != nil {
		t.Fatal(err)
	}
	if e.ID == "" || e.SpecVersion != "1.0" || e.TenantID != "tenant-a" || string(e.Data) != `{"id":"cust-1"}` {
		t.Fatalf("bad event %+v", e)
	}
}

func TestNewCopiesPayloadLegalEntity(t *testing.T) {
	typ := Type("sales", "order", "confirmed", 1)
	cases := []struct {
		name string
		data any
		want string
	}{
		{"camel", map[string]string{"legalEntityId": "le-1"}, "le-1"},
		{"snake", map[string]string{"legal_entity_id": "le-2"}, "le-2"},
		{"none", map[string]string{"id": "x"}, ""},
		{"not an object", []string{"a"}, ""},
		{"not a string", map[string]int{"legalEntityId": 7}, ""},
	}
	for _, c := range cases {
		e, err := New(typ, "/services/sales", "x", "tenant-a", c.data)
		if err != nil {
			t.Fatal(err)
		}
		if e.LegalEntityID != c.want {
			t.Errorf("%s: legal entity %q, want %q", c.name, e.LegalEntityID, c.want)
		}
	}
	e, _ := New(typ, "/services/sales", "x", "tenant-a", map[string]string{"id": "x"})
	raw, _ := json.Marshal(e)
	if strings.Contains(string(raw), "legalentityid") {
		t.Errorf("empty legal entity serialised: %s", raw)
	}
}

func TestTraceFieldsAreOmittedWhenUnsetAndRoundTripWhenSet(t *testing.T) {
	typ := Type("sales", "order", "confirmed", 1)
	e, _ := New(typ, "/services/sales", "x", "tenant-a", map[string]string{"id": "x"})
	raw, _ := json.Marshal(e)
	for _, name := range []string{"traceparent", "sequence", "dataschema"} {
		if strings.Contains(string(raw), name) {
			t.Errorf("unset %s serialised: %s", name, raw)
		}
	}
	e.Traceparent, e.Sequence, e.DataSchema = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01", 5, DataSchema(typ)
	raw, _ = json.Marshal(e)
	var back Event
	if err := json.Unmarshal(raw, &back); err != nil || back.Traceparent != e.Traceparent || back.Sequence != 5 || back.DataSchema != "https://schemas.depot.dev/"+typ+".json" {
		t.Fatalf("round trip %+v err %v", back, err)
	}
}
