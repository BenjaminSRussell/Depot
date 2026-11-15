package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/platform/ids"
	evts "github.com/BenjaminSRussell/passage/services/warehouse/internal/events"
)

func TestStore_ConfirmShippedPublishesShipmentShippedOnce(t *testing.T) {
	st, ctx, wh := setup(t)
	le := "00000000-0000-0000-0000-0000000000e1"
	sh := ids.New()
	at := time.Date(2026, 11, 10, 8, 30, 0, 0, time.UTC)
	c := ShipConfirmation{ShipmentID: sh, LegalEntityID: le, WarehouseID: wh, CarrierCode: "UPSN", ShippedAt: at,
		SSCCs: []string{"376104250000000017"}, ConfirmedBy: "u1"}
	out, replayed, err := st.ConfirmShipped(ctx, key("ship-"+sh), c)
	if err != nil || replayed || out.CreatedAt.IsZero() {
		t.Fatalf("ConfirmShipped = %+v, %v, %v", out, replayed, err)
	}
	if _, replayed, err := st.ConfirmShipped(ctx, key("ship-"+sh), c); err != nil || !replayed {
		t.Fatalf("repeat of the key = %v, %v", replayed, err)
	}
	if _, _, err := st.ConfirmShipped(ctx, key("other-"+sh), c); !errors.Is(err, ErrShipmentShipped) {
		t.Fatalf("second confirmation of the shipment = %v, want ErrShipmentShipped", err)
	}
	var raw []byte
	var n int
	if err := adminOf(st).QueryRow(context.Background(), `select count(*), max(payload::text) from outbox where type = $1 and aggregate_id = $2`,
		evts.ShipmentShipped, sh).Scan(&n, &raw); err != nil || n != 1 {
		t.Fatalf("shipment.shipped rows = %d, %v", n, err)
	}
	var env struct {
		LegalEntityID string                   `json:"legalentityid"`
		Data          evts.ShipmentShippedData `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	if env.LegalEntityID != le || env.Data.ShipmentID != sh || env.Data.ShippedAt != "2026-11-10T08:30:00Z" ||
		len(env.Data.Containers) != 1 || env.Data.Containers[0].SSCC != "376104250000000017" || env.Data.CarrierCode != "UPSN" {
		t.Fatalf("event %+v", env)
	}
	got, err := st.GetShipConfirmation(ctx, sh)
	if err != nil || got.WarehouseID != wh || !got.ShippedAt.Equal(at) {
		t.Fatalf("GetShipConfirmation = %+v, %v", got, err)
	}
}

func TestStore_ConfirmShippedCarriesThePackHierarchyInTheEvent(t *testing.T) {
	st, ctx, wh := setup(t)
	le := "00000000-0000-0000-0000-0000000000e1"
	sh := ids.New()
	line := ids.New()
	at := time.Date(2026, 11, 10, 8, 30, 0, 0, time.UTC)
	c := ShipConfirmation{ShipmentID: sh, LegalEntityID: le, WarehouseID: wh, ShippedAt: at, ConfirmedBy: "u1",
		SSCCs: []string{"376104250000000017", "376104250000000024"},
		Containers: []ShipContainer{
			{SSCC: "376104250000000017", GrossKg: "412.5"},
			{SSCC: "376104250000000024", ParentSSCC: "376104250000000017", GrossKg: "12.25", Lines: []ShipLine{{ShipmentLineID: line, Qty: "6"}}},
		}}
	if _, _, err := st.ConfirmShipped(ctx, key("pack-"+sh), c); err != nil {
		t.Fatal(err)
	}
	var raw []byte
	if err := adminOf(st).QueryRow(context.Background(), `select payload::text from outbox where type = $1 and aggregate_id = $2`, evts.ShipmentShipped, sh).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var env struct {
		Data evts.ShipmentShippedData `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		t.Fatal(err)
	}
	got := env.Data.Containers
	if len(got) != 2 || got[0].ParentSSCC != "" || got[0].GrossKg != "412.5" || len(got[0].Lines) != 0 ||
		got[1].ParentSSCC != "376104250000000017" || got[1].GrossKg != "12.25" || len(got[1].Lines) != 1 || got[1].Lines[0].ShipmentLineID != line || got[1].Lines[0].Qty != "6" {
		t.Fatalf("event containers %+v", got)
	}
	read, err := st.GetShipConfirmation(ctx, sh)
	if err != nil || len(read.Containers) != 2 || read.Containers[1].Lines[0].Qty != "6" {
		t.Fatalf("read back %+v %v", read, err)
	}
	// A confirmation that names only SSCCs keeps its old event shape: no pack data.
	other := ids.New()
	if _, _, err := st.ConfirmShipped(ctx, key("plain-"+other), ShipConfirmation{ShipmentID: other, LegalEntityID: le, WarehouseID: wh, ShippedAt: at, ConfirmedBy: "u1", SSCCs: []string{"376104250000000017"}}); err != nil {
		t.Fatal(err)
	}
	if err := adminOf(st).QueryRow(context.Background(), `select payload::text from outbox where type = $1 and aggregate_id = $2`, evts.ShipmentShipped, other).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "parent_sscc") || strings.Contains(string(raw), "gross_kg") || strings.Contains(string(raw), `"lines"`) {
		t.Fatalf("a plain confirmation carries pack data: %s", raw)
	}
}
