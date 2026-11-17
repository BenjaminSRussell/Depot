package api

import (
	"context"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/warehouse/internal/domain"
	"github.com/BenjaminSRussell/depot/services/warehouse/internal/store"
)

func (f *fakeStore) ConfirmShipped(_ context.Context, idem store.Idem, c store.ShipConfirmation) (store.ShipConfirmation, bool, error) {
	f.calls++
	f.idem, f.ship = idem, c
	return c, f.replayed, f.err
}

func (f *fakeStore) GetShipConfirmation(_ context.Context, id string) (store.ShipConfirmation, error) {
	f.calls++
	return store.ShipConfirmation{ShipmentID: id}, f.err
}

const shipLE = "0190a5f2-7b1c-7d3e-8a4f-000000000e11"

func ship(h *httptest.ResponseRecorder, api *API, path, duties, le, body string) *httptest.ResponseRecorder {
	router := httpx.NewRouter()
	api.Register(router)
	r := httptest.NewRequest("POST", path, strings.NewReader(body))
	r.Header.Set("Content-Type", "application/json")
	r.Header.Set("X-Depot-Tenant", testkit.TenantID(1))
	r.Header.Set("X-Depot-User", "u1")
	r.Header.Set("X-Depot-Duties", duties)
	if le != "" {
		r.Header.Set(tenant.HeaderLegalEntity, le)
	}
	r.Header.Set("Idempotency-Key", "k1")
	router.ServeHTTP(h, r)
	return h
}

func TestConfirmShipped_RecordsTheConfirmationInTheCallersLegalEntity(t *testing.T) {
	fs := &fakeStore{}
	sh, wh := ids.New(), ids.New()
	body := `{"warehouseId":"` + wh + `","carrierCode":"UPSN","shippedAt":"2026-11-10T08:30:00Z","ssccs":["376104250000000017"]}`
	rec := ship(httptest.NewRecorder(), NewWithStore(fs, time.Minute), "/v1/warehouse/shipments/"+sh+":ship", dutyShipConfirm, shipLE, body)
	if rec.Code != 201 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if fs.ship.ShipmentID != sh || fs.ship.LegalEntityID != shipLE || fs.ship.WarehouseID != wh || fs.ship.CarrierCode != "UPSN" ||
		!fs.ship.ShippedAt.Equal(time.Date(2026, 11, 10, 8, 30, 0, 0, time.UTC)) || len(fs.ship.SSCCs) != 1 || fs.ship.ConfirmedBy != "u1" {
		t.Fatalf("store got %+v", fs.ship)
	}
}

func TestConfirmShipped_Refusals(t *testing.T) {
	sh, wh := ids.New(), ids.New()
	path := "/v1/warehouse/shipments/" + sh + ":ship"
	future := time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339)
	cases := []struct {
		name, path, duties, le, body string
		status                       int
		field                        string
	}{
		{"needs the ship-confirm duty", path, dutyReceive, shipLE, `{"warehouseId":"` + wh + `"}`, 403, ""},
		{"shipment must be a uuid", "/v1/warehouse/shipments/nope:ship", dutyShipConfirm, shipLE, `{"warehouseId":"` + wh + `"}`, 400, "id"},
		{"warehouse must be a uuid", path, dutyShipConfirm, shipLE, `{"warehouseId":"w1"}`, 400, "warehouseId"},
		{"legal entity outside the caller's scope", path, dutyShipConfirm, shipLE, `{"warehouseId":"` + wh + `","legalEntityId":"` + ids.New() + `"}`, 400, "legalEntityId"},
		{"a caller with no legal entity must name one", path, dutyShipConfirm, "", `{"warehouseId":"` + wh + `"}`, 400, "legalEntityId"},
		{"carrier is a SCAC", path, dutyShipConfirm, shipLE, `{"warehouseId":"` + wh + `","carrierCode":"ups"}`, 400, "carrierCode"},
		{"not in the future", path, dutyShipConfirm, shipLE, `{"warehouseId":"` + wh + `","shippedAt":"` + future + `"}`, 400, "shippedAt"},
		{"SSCC check digit", path, dutyShipConfirm, shipLE, `{"warehouseId":"` + wh + `","ssccs":["376104250000000016"]}`, 400, "ssccs[0]"},
		{"SSCC listed twice", path, dutyShipConfirm, shipLE, `{"warehouseId":"` + wh + `","ssccs":["376104250000000017","376104250000000017"]}`, 400, "ssccs[1]"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := ship(httptest.NewRecorder(), NewWithStore(fs, time.Minute), c.path, c.duties, c.le, c.body)
			if rec.Code != c.status {
				t.Fatalf("status %d, want %d: %s", rec.Code, c.status, rec.Body)
			}
			if fs.calls != 0 {
				t.Fatal("a refused request reached the store")
			}
			if c.field != "" {
				var p struct {
					Errors map[string]string `json:"errors"`
				}
				_ = json.Unmarshal(rec.Body.Bytes(), &p)
				if _, ok := p.Errors[c.field]; !ok {
					t.Fatalf("no error for %s: %s", c.field, rec.Body)
				}
			}
		})
	}
}

func TestConfirmShipped_SecondConfirmationIsRefusedWithItsOwnCode(t *testing.T) {
	fs := &fakeStore{err: store.ErrShipmentShipped}
	rec := ship(httptest.NewRecorder(), NewWithStore(fs, time.Minute), "/v1/warehouse/shipments/"+ids.New()+":ship", dutyShipConfirm, shipLE, `{"warehouseId":"`+ids.New()+`"}`)
	if rec.Code != 409 || !strings.Contains(rec.Body.String(), codeShipmentShipped) {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
}

const (
	pallet = "376104250000000017"
	carton = "376104250000000024"
	line1  = "0190a5f2-7b1c-7d3e-8a4f-000000000a01"
)

func TestConfirmShipped_RecordsThePackHierarchyTheWarehouseSaw(t *testing.T) {
	if !domain.ValidSSCC(pallet) || !domain.ValidSSCC(carton) {
		t.Fatalf("the fixture SSCCs %s and %s have no valid check digit", pallet, carton)
	}
	fs := &fakeStore{}
	sh, wh := ids.New(), ids.New()
	body := `{"warehouseId":"` + wh + `","containers":[
		{"sscc":"` + pallet + `","grossKg":"412.5"},
		{"sscc":"` + carton + `","parentSscc":"` + pallet + `","grossKg":"12.25","lines":[{"shipmentLineId":"` + strings.ToUpper(line1) + `","qty":"6"}]}]}`
	rec := ship(httptest.NewRecorder(), NewWithStore(fs, time.Minute), "/v1/warehouse/shipments/"+sh+":ship", dutyShipConfirm, shipLE, body)
	if rec.Code != 201 {
		t.Fatalf("status %d: %s", rec.Code, rec.Body)
	}
	if len(fs.ship.SSCCs) != 2 || fs.ship.SSCCs[0] != pallet || fs.ship.SSCCs[1] != carton || len(fs.ship.Containers) != 2 {
		t.Fatalf("store got %+v", fs.ship)
	}
	c := fs.ship.Containers[1]
	if c.ParentSSCC != pallet || c.GrossKg != "12.25" || len(c.Lines) != 1 || c.Lines[0].ShipmentLineID != line1 || c.Lines[0].Qty != "6" {
		t.Fatalf("carton %+v", c)
	}
}

func TestConfirmShipped_PackRefusals(t *testing.T) {
	sh, wh := ids.New(), ids.New()
	path := "/v1/warehouse/shipments/" + sh + ":ship"
	box := func(extra string) string {
		return `{"sscc":"` + carton + `","lines":[{"shipmentLineId":"` + line1 + `","qty":"1"}]` + extra + `}`
	}
	cases := []struct{ name, containers, field string }{
		{"ssccs and containers together", `[` + box("") + `],"ssccs":["` + pallet + `"]`, "containers"},
		{"a bad check digit", `[{"sscc":"376104250000000016","lines":[{"shipmentLineId":"` + line1 + `","qty":"1"}]}]`, "containers[0].sscc"},
		{"the same SSCC twice", `[` + box("") + `,` + box("") + `]`, "containers[1].sscc"},
		{"a carton on a container that is not in the confirmation", `[` + box(`,"parentSscc":"`+pallet+`"`) + `]`, "containers[0].parentSscc"},
		{"a container on itself", `[` + box(`,"parentSscc":"`+carton+`"`) + `]`, "containers[0].parentSscc"},
		{"a leaf container with no lines", `[{"sscc":"` + carton + `"}]`, "containers[0].lines"},
		{"a quantity of zero", `[{"sscc":"` + carton + `","lines":[{"shipmentLineId":"` + line1 + `","qty":"0.00"}]}]`, "containers[0].lines[0].qty"},
		{"a line that is not an id", `[{"sscc":"` + carton + `","lines":[{"shipmentLineId":"x","qty":"1"}]}]`, "containers[0].lines[0].shipmentLineId"},
		{"a weight that is not a number", `[` + box(`,"grossKg":"heavy"`) + `]`, "containers[0].grossKg"},
		{"a pallet on a pallet", `[{"sscc":"` + pallet + `","parentSscc":"` + carton + `","lines":[{"shipmentLineId":"` + line1 + `","qty":"1"}]},` + box(`,"parentSscc":"`+pallet+`"`) + `]`, "containers[1].parentSscc"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			fs := &fakeStore{}
			rec := ship(httptest.NewRecorder(), NewWithStore(fs, time.Minute), path, dutyShipConfirm, shipLE, `{"warehouseId":"`+wh+`","containers":`+c.containers+`}`)
			if rec.Code != 400 || fs.calls != 0 {
				t.Fatalf("status %d calls %d: %s", rec.Code, fs.calls, rec.Body)
			}
			var p struct {
				Errors map[string]string `json:"errors"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &p)
			if _, ok := p.Errors[c.field]; !ok {
				t.Fatalf("no error for %s: %s", c.field, rec.Body)
			}
		})
	}
}
