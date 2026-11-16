package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/passage/services/ledger/internal/store"
)

const testPartner = "0c5d3b0e-6b0a-4c43-9d57-3c1a3a7a1d11"

func TestPartnerBalances_ValidatesAndReads(t *testing.T) {
	fs := &fakeStore{partners: store.PartnerBalances{LedgerID: testLedger, Currency: "USD", Rows: []store.PartnerBalanceRow{
		{Account: "2100", TradingPartnerID: testPartner, Debit: "0.00", Credit: "10.00", Net: "-10.00", LineCount: 1}}}}
	h := server(fs)
	if rec := call(h, http.MethodGet, "/v1/ledger/balances/by-partner", "", dutyBalanceRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("missing ledger: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/balances/by-partner?ledger="+testLedger+"&trading_partner=nope", "", dutyBalanceRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad partner: %d", rec.Code)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/balances/by-partner?ledger="+testLedger+"&period_from=2026-3", "", dutyBalanceRead, nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("bad period: %d", rec.Code)
	}
	rec := call(h, http.MethodGet, "/v1/ledger/balances/by-partner?ledger="+testLedger+"&period_to=2026-03&trading_partner="+strings.ToUpper(testPartner), "", dutyBalanceRead, nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), testPartner) {
		t.Fatalf("read: %d %s", rec.Code, rec.Body.String())
	}
	if fs.partnerFilter.TradingPartnerID != testPartner || fs.partnerFilter.PeriodTo != "2026-03" {
		t.Fatalf("filter passed down: %+v", fs.partnerFilter)
	}
	if rec := call(h, http.MethodGet, "/v1/ledger/balances/by-partner?ledger="+testLedger, "", dutyPost, nil); rec.Code != http.StatusForbidden {
		t.Fatalf("the balance duty is required: %d", rec.Code)
	}
}

func TestPostEntry_RefusesAMalformedTradingPartner(t *testing.T) {
	body := strings.Replace(entryBody, `"debitCredit":"C"`, `"tradingPartnerId":"not-a-uuid","debitCredit":"C"`, 1)
	if body == entryBody {
		t.Fatal("test fixture has no credit line to change")
	}
	rec := call(server(&fakeStore{}), http.MethodPost, "/v1/ledger/journal-entries", body, dutyPost, map[string]string{"Idempotency-Key": "ic-1"})
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "tradingPartnerId") {
		t.Fatalf("a malformed partner must be a 400 naming the field: %d %s", rec.Code, rec.Body.String())
	}
}
