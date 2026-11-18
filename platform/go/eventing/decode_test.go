package eventing_test

import (
	"errors"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/eventing"
)

func TestDecodeBinaryMode(t *testing.T) {
	r := eventing.Record{
		Value: []byte(`{"receipt_id":"r1"}`),
		Headers: map[string]string{
			"ce_specversion": "1.0", "ce_id": "e1", "ce_type": eventType, "ce_source": "/depot/receiving", "ce_tenantid": tenantA,
			"ce_correlationid": "c1", "ce_time": "2026-10-31T10:00:00Z", "tenantid": tenantA,
		},
	}
	evt, err := eventing.Decode(r)
	if err != nil {
		t.Fatal(err)
	}
	if evt.ID != "e1" || evt.Type != eventType || evt.TenantID != tenantA || evt.CorrelationID != "c1" || string(evt.Data) != `{"receipt_id":"r1"}` || evt.Time.IsZero() {
		t.Fatalf("event = %+v", evt)
	}
}

func TestDecodeReadsTraceFieldsFromBinaryHeadersAndStructuredJSON(t *testing.T) {
	const tp = "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
	binary := func(sequence string) eventing.Record {
		return eventing.Record{Value: []byte(`{"receipt_id":"r1"}`), Headers: map[string]string{
			"ce_specversion": "1.0", "ce_id": "e1", "ce_type": eventType, "ce_tenantid": tenantA, "tenantid": tenantA,
			"ce_traceparent": tp, "ce_sequence": sequence, "ce_dataschema": "https://schemas.depot.dev/x.json",
		}}
	}
	for sequence, want := range map[string]int64{"4": 4, "": 0, "abc": 0, "-4": 0} {
		evt, err := eventing.Decode(binary(sequence))
		if err != nil || evt.Traceparent != tp || evt.Sequence != want || evt.DataSchema != "https://schemas.depot.dev/x.json" {
			t.Fatalf("ce_sequence %q: event %+v err %v", sequence, evt, err)
		}
	}
	structured := record(t, 1, "e1", eventType, tenantA)
	structured.Value = []byte(`{"specversion":"1.0","id":"e1","type":"` + eventType + `","tenantid":"` + tenantA + `","data":{},"traceparent":"` + tp + `","sequence":9,"dataschema":"d"}`)
	evt, err := eventing.Decode(structured)
	if err != nil || evt.Traceparent != tp || evt.Sequence != 9 || evt.DataSchema != "d" {
		t.Fatalf("structured event %+v err %v", evt, err)
	}
}

func TestDecodeRefusesMalformedEvents(t *testing.T) {
	good := record(t, 1, "e1", eventType, tenantA)
	cases := map[string]func(*eventing.Record){
		"bad json": func(r *eventing.Record) { r.Value = []byte("[") },
		"no data": func(r *eventing.Record) {
			r.Value = []byte(`{"specversion":"1.0","id":"e1","type":"` + eventType + `","tenantid":"` + tenantA + `"}`)
		},
		"tenant header off": func(r *eventing.Record) { r.Headers["tenantid"] = "22222222-2222-4222-8222-222222222222" },
		"binary no id": func(r *eventing.Record) {
			r.Headers = map[string]string{"ce_specversion": "1.0", "ce_type": eventType, "ce_tenantid": tenantA}
			r.Value = []byte(`{}`)
		},
	}
	for name, mutate := range cases {
		r := good
		r.Headers = map[string]string{"tenantid": tenantA}
		mutate(&r)
		if _, err := eventing.Decode(r); !errors.Is(err, eventing.ErrMalformedEvent) {
			t.Errorf("%s: err = %v, want ErrMalformedEvent", name, err)
		}
	}
	if _, err := eventing.Decode(good); err != nil {
		t.Fatalf("structured event refused: %v", err)
	}
}
