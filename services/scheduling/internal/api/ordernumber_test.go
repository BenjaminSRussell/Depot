package api

import (
	"net/http"
	"strings"
	"testing"
)

// The order number is the third dispatch key (FR-23.5); POST /operations
// passes it to the store and refuses one that is blank, padded or too long.
func TestCreateOperation_PassesOrderNumber(t *testing.T) {
	f := &fakeStore{}
	rec := do(newRouter(f), http.MethodPost, "/v1/scheduling/operations", operationBody(`,"orderNumber":"ORD-0001"`), allDuties, "op-num")
	if rec.Code != http.StatusCreated || f.lastOp.OrderNumber == nil || *f.lastOp.OrderNumber != "ORD-0001" {
		t.Fatalf("status %d store got %+v", rec.Code, f.lastOp)
	}
	f = &fakeStore{}
	rec = do(newRouter(f), http.MethodPost, "/v1/scheduling/operations", operationBody(""), allDuties, "op-nonum")
	if rec.Code != http.StatusCreated || f.lastOp.OrderNumber != nil {
		t.Fatalf("status %d store got %+v, want no order number", rec.Code, f.lastOp)
	}
}

func TestCreateOperation_ValidatesOrderNumber(t *testing.T) {
	cases := []struct{ name, extra string }{
		{"empty", `,"orderNumber":""`},
		{"blank", `,"orderNumber":"   "`},
		{"padded", `,"orderNumber":" ORD-1"`},
		{"41 characters", `,"orderNumber":"` + strings.Repeat("9", 41) + `"`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			rec := do(newRouter(&fakeStore{}), http.MethodPost, "/v1/scheduling/operations", operationBody(c.extra), allDuties, "k-"+c.name)
			if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), `"orderNumber"`) {
				t.Fatalf("status %d body %s, want 400 naming orderNumber", rec.Code, rec.Body)
			}
		})
	}
}
