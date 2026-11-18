package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/ids"
)

func (f *fakeStore) WarehouseIssueCost(_ context.Context, legalEntityID, productID, warehouse string) (string, decimal.Decimal, error) {
	f.claim.costAsked = [3]string{legalEntityID, productID, warehouse}
	return "USD", decimal.RequireFromString(f.claim.costIs), f.claim.costErr
}

func costPath(le, product, warehouse string) string {
	return "/v1/inventory/issue-cost?" + url.Values{"legalEntityId": {le}, "productId": {product}, "warehouse": {warehouse}}.Encode()
}

func TestIssueCost_ReturnsTheRunningAverage(t *testing.T) {
	fs := &fakeStore{claim: issueClaimFake{costIs: "12.5"}}
	le, product := ids.New(), ids.New()
	rec := do(t, newServer(t, fs), http.MethodGet, costPath(le, product, "W1"), "", []string{"inventory.issue.claim"}, nil)
	var got struct{ Currency, UnitCost string }
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil || rec.Code != http.StatusOK || got.Currency != "USD" || got.UnitCost != "12.500000" {
		t.Fatalf("%d %s (%v)", rec.Code, rec.Body, err)
	}
	if fs.claim.costAsked != [3]string{le, product, "W1"} {
		t.Fatalf("store asked for %v", fs.claim.costAsked)
	}
}

func TestIssueCost_RefusesBadIds(t *testing.T) {
	cases := map[string]struct{ path, field string }{
		"legal entity": {costPath("x", ids.New(), "W1"), "legalEntityId"},
		"product":      {costPath(ids.New(), "", "W1"), "productId"},
		"warehouse":    {costPath(ids.New(), ids.New(), ""), "warehouse"},
	}
	for name, c := range cases {
		rec := do(t, newServer(t, &fakeStore{claim: issueClaimFake{costIs: "1"}}), http.MethodGet, c.path, "", []string{"inventory.issue.claim"}, nil)
		if p := decodeProblem(t, rec); rec.Code != http.StatusBadRequest || p.Errors[c.field] == "" {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}
}

func TestIssueCost_OnlyTheServiceDutyReachesIt(t *testing.T) {
	for duty, want := range map[string]int{"inventory.issue.claim": http.StatusOK, "inventory.read": http.StatusForbidden} {
		rec := do(t, newServer(t, &fakeStore{claim: issueClaimFake{costIs: "1"}}), http.MethodGet, costPath(ids.New(), ids.New(), "W1"), "", []string{duty}, nil)
		if rec.Code != want {
			t.Errorf("%s: %d, want %d", duty, rec.Code, want)
		}
	}
}
