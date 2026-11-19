package api

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/receivables/internal/store"
)

func (f *fakeStore) GetFXSetting(_ context.Context, le string) (store.FXSetting, error) {
	if f.fx.LegalEntityID == le {
		return f.fx, nil
	}
	return store.DefaultFXSetting(le), nil
}

func (f *fakeStore) PutFXSetting(_ context.Context, in store.FXSetting) (store.FXSetting, error) {
	f.fx = in
	return in, nil
}

func (f *fakeStore) ReversePayment(_ context.Context, id string, version int) (store.Payment, error) {
	f.reversed, f.lastVersion = id, version
	return store.Payment{ID: id, Status: "REVERSED", Version: version + 1}, nil
}

func (f *fakeStore) UnapplyPayment(_ context.Context, id string, version int, reason string) (store.UnapplyResult, error) {
	f.reversed, f.lastVersion = id, version
	return store.UnapplyResult{Payment: store.Payment{ID: id, Status: "RECEIVED", Version: version + 1}}, nil
}

func (f *fakeStore) UnapplySettlement(_ context.Context, paymentID, settlementID string, version int, reason string) (store.UnapplyResult, error) {
	f.reversed, f.lastVersion, f.lastSettlement = paymentID, version, settlementID
	return store.UnapplyResult{Payment: store.Payment{ID: paymentID, Status: "ON_ACCOUNT", Version: version + 1}}, nil
}

func (f *fakeStore) ReverseApplication(_ context.Context, paymentID string, version int, req store.ReverseApplicationRequest, key string) (store.ReverseApplicationResult, error) {
	f.reversed, f.lastVersion, f.lastSettlement, f.lastKey = paymentID, version, req.ApplicationID, key
	if f.reverseErr != nil {
		return store.ReverseApplicationResult{}, f.reverseErr
	}
	return store.ReverseApplicationResult{ApplicationID: req.ApplicationID, ReversalJournalEntryID: "je-1", Payment: store.Payment{ID: paymentID, Version: version + 1}}, nil
}

func (f *fakeStore) CheckReverseApplication(_ context.Context, paymentID string, req store.ReverseApplicationRequest) (store.ReverseEligibility, error) {
	f.reversed, f.lastSettlement = paymentID, req.ApplicationID
	return f.eligibility, f.reverseErr
}

func (f *fakeStore) ReverseCreditSettlement(_ context.Context, batchID, reason string) (store.CreditReversalResult, error) {
	f.lastSettlement = batchID
	return store.CreditReversalResult{BatchID: batchID, CreditStatus: "OPEN"}, nil
}

func (f *fakeStore) ListPaymentSettlements(context.Context, string) ([]store.PaymentSettlement, error) {
	return []store.PaymentSettlement{}, nil
}

func (f *fakeStore) ListCreditSettlements(context.Context, store.CreditSettlementFilter, int) ([]store.CreditSettlementBatch, error) {
	return []store.CreditSettlementBatch{}, nil
}

func TestUnapplySettlement_NamesOneSettlementAndCreditReversalNeedsTheReverseDuty(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	path := "/v1/receivables/payments/" + testLE + ":unapply"
	if code := do(h, http.MethodPost, path, `{"settlementId":"nope"}`, "receivables.payment.reverse", map[string]string{"If-Match": `"3"`}).Code; code != http.StatusBadRequest {
		t.Errorf("a bad settlement id: %d", code)
	}
	body := `{"settlementId":"` + testLE + `"}`
	if rec := do(h, http.MethodPost, path, body, "receivables.payment.reverse", map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusOK || f.lastSettlement != testLE || f.lastVersion != 3 {
		t.Errorf("unapply one: %d %s", rec.Code, rec.Body.String())
	}
	credit := "/v1/receivables/credit-settlements/" + testLE + ":reverse"
	if code := do(h, http.MethodPost, credit, `{}`, "receivables.payment.settle", nil).Code; code != http.StatusForbidden {
		t.Errorf("settle duty may not reverse: %d", code)
	}
	f.lastSettlement = ""
	if rec := do(h, http.MethodPost, credit, `{"reason":"wrong invoice"}`, "receivables.payment.reverse", nil); rec.Code != http.StatusOK || f.lastSettlement != testLE {
		t.Errorf("reverse credit settlement: %d %s", rec.Code, rec.Body.String())
	}
	if code := do(h, http.MethodPost, "/v1/receivables/credit-settlements/"+testLE+":refund", `{}`, "receivables.payment.reverse", nil).Code; code != http.StatusNotFound {
		t.Errorf("unknown action: %d", code)
	}
	if code := do(h, http.MethodGet, "/v1/receivables/credit-settlements?customerId=x", ``, "receivables.view", nil).Code; code != http.StatusBadRequest {
		t.Errorf("bad filter: %d", code)
	}
	if code := do(h, http.MethodGet, "/v1/receivables/payments/"+testLE+"/settlements", ``, "receivables.view", nil).Code; code != http.StatusOK {
		t.Errorf("list: %d", code)
	}
}

func TestUnapplyPayment_NeedsTheReverseDutyAndAVersion(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	path := "/v1/receivables/payments/" + testLE + ":unapply"
	if code := do(h, http.MethodPost, path, `{}`, "receivables.payment.write", map[string]string{"If-Match": `"3"`}).Code; code != http.StatusForbidden {
		t.Errorf("payment write may not unapply: %d", code)
	}
	if code := do(h, http.MethodPost, path, `{}`, "receivables.payment.reverse", nil).Code; code == http.StatusOK {
		t.Errorf("unapply without If-Match: %d", code)
	}
	if rec := do(h, http.MethodPost, path, `{"reason":"applied to the wrong invoice"}`, "receivables.payment.reverse", map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusOK || f.lastVersion != 3 {
		t.Errorf("unapply: %d %s (version %d)", rec.Code, rec.Body.String(), f.lastVersion)
	}
}

func TestFXSettingRoutes(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	put := func(body, duty string) int {
		return do(h, http.MethodPut, "/v1/receivables/fx-settings", body, duty, nil).Code
	}
	good := `{"legalEntityId":"` + testLE + `","crossCurrencyEnabled":true,"functionalCurrency":"USD"}`
	if code := put(good, "receivables.view"); code != http.StatusForbidden {
		t.Errorf("a viewer may not write: %d", code)
	}
	for name, body := range map[string]string{
		"enabled without a functional currency": `{"legalEntityId":"` + testLE + `","crossCurrencyEnabled":true}`,
		"lower case currency":                   `{"legalEntityId":"` + testLE + `","functionalCurrency":"usd"}`,
		"bad account":                           `{"legalEntityId":"` + testLE + `","functionalCurrency":"USD","gainAccount":"71 00"}`,
		"no legal entity":                       `{"functionalCurrency":"USD"}`,
	} {
		if code := put(body, "receivables.fxsetting.manage"); code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400", name, code)
		}
	}
	rec := do(h, http.MethodPut, "/v1/receivables/fx-settings", good, "receivables.fxsetting.manage", nil)
	var got store.FXSetting
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.GainAccount != "7100" || got.LossAccount != "7200" || !got.CrossCurrencyEnabled {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	rec = do(h, http.MethodGet, "/v1/receivables/fx-settings?legalEntityId="+testLE, "", "receivables.view", nil)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.FunctionalCurrency != "USD" {
		t.Errorf("get: %d %s", rec.Code, rec.Body.String())
	}
	if rec = do(h, http.MethodGet, "/v1/receivables/fx-settings", "", "receivables.view", nil); rec.Code != http.StatusBadRequest {
		t.Errorf("get without a legal entity: %d", rec.Code)
	}
}

func TestReversePayment_NeedsItsOwnDutyAndAVersion(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	id := ids.New()
	path := "/v1/receivables/payments/" + id + ":reverse"
	if rec := do(h, http.MethodPost, path, "", "receivables.payment.post", map[string]string{"If-Match": `"3"`}); rec.Code != http.StatusForbidden {
		t.Errorf("the payment duty may not reverse: %d", rec.Code)
	}
	if rec := do(h, http.MethodPost, path, "", "receivables.payment.reverse", nil); rec.Code == http.StatusOK {
		t.Errorf("no If-Match was accepted")
	}
	rec := do(h, http.MethodPost, path, "", "receivables.payment.reverse", map[string]string{"If-Match": `"3"`})
	if rec.Code != http.StatusOK || f.reversed != id || f.lastVersion != 3 {
		t.Errorf("reverse: %d %s (%s v%d)", rec.Code, rec.Body.String(), f.reversed, f.lastVersion)
	}
}

func TestApplyPayment_ItemAmountReachesTheStore(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	body := `{"settlements":[{"openItemId":"` + ids.New() + `","amount":"90.00","itemAmount":"100.00"}],"onAccount":"0"}`
	rec := do(h, http.MethodPost, "/v1/receivables/payments/"+ids.New()+":apply", body, "receivables.payment.post", map[string]string{"If-Match": `"1"`})
	if rec.Code != http.StatusOK || len(f.lastPlan.Allocations) != 1 || f.lastPlan.Allocations[0].ItemCurrencyAmount.String() != "100" {
		t.Fatalf("apply: %d %s %+v", rec.Code, rec.Body.String(), f.lastPlan)
	}
}

func TestReverseApplication_NeedsTheDutyVersionKeyAndAValidBody(t *testing.T) {
	f := &fakeStore{}
	h := router(f)
	path := "/v1/receivables/payments/" + ids.New() + ":reverse-application"
	appID := ids.New()
	body := `{"applicationId":"` + appID + `","reason":"applied to the wrong invoice"}`
	headers := map[string]string{"If-Match": `"3"`, "Idempotency-Key": "rev-key-1"}
	if code := do(h, http.MethodPost, path, body, "receivables.payment.reverse", headers).Code; code != http.StatusForbidden {
		t.Errorf("without receivables.payment.post: %d", code)
	}
	if code := do(h, http.MethodPost, path, body, "receivables.payment.post", map[string]string{"Idempotency-Key": "rev-key-1"}).Code; code != http.StatusPreconditionRequired {
		t.Errorf("without If-Match: %d", code)
	}
	if code := do(h, http.MethodPost, path, body, "receivables.payment.post", map[string]string{"If-Match": `"3"`}).Code; code != http.StatusBadRequest {
		t.Errorf("without Idempotency-Key: %d", code)
	}
	for name, bad := range map[string]string{
		"application id not a uuid": `{"applicationId":"nope","reason":"x"}`,
		"reason missing":            `{"applicationId":"` + appID + `"}`,
		"reason blank":              `{"applicationId":"` + appID + `","reason":"   "}`,
		"reason too long":           `{"applicationId":"` + appID + `","reason":"` + strings.Repeat("x", 501) + `"}`,
		"dryRun with a bad id":      `{"applicationId":"nope","reason":"x","dryRun":true}`,
	} {
		if code := do(h, http.MethodPost, path, bad, "receivables.payment.post", headers).Code; code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, code)
		}
	}
	rec := do(h, http.MethodPost, path, body, "receivables.payment.post", headers)
	if rec.Code != http.StatusOK || rec.Header().Get("ETag") != `"4"` || f.lastVersion != 3 || f.lastSettlement != appID || f.lastKey != "rev-key-1" {
		t.Fatalf("reverse application: %d %s etag %s (v%d app %s key %s)", rec.Code, rec.Body.String(), rec.Header().Get("ETag"), f.lastVersion, f.lastSettlement, f.lastKey)
	}
}

func TestReverseApplication_ProblemCodesKeepTheirStatus(t *testing.T) {
	appID := ids.New()
	body := `{"applicationId":"` + appID + `","reason":"x"}`
	headers := map[string]string{"If-Match": `"3"`, "Idempotency-Key": "rev-key-1"}
	for _, tt := range []struct {
		code   string
		status int
	}{
		{store.CodeReversalReconciled, http.StatusConflict},
		{store.CodeReversalLedgerUnavailable, http.StatusServiceUnavailable},
		{store.CodeReversalPeriodClosed, http.StatusConflict},
		{store.CodeReversalItemChanged, http.StatusConflict},
	} {
		f := &fakeStore{reverseErr: httpx.NewProblem(tt.status, tt.code, "refused")}
		rec := do(router(f), http.MethodPost, "/v1/receivables/payments/"+ids.New()+":reverse-application", body, "receivables.payment.post", headers)
		var got httpx.Problem
		if rec.Code != tt.status || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Code != tt.code {
			t.Errorf("%s: %d %s", tt.code, rec.Code, rec.Body.String())
		}
	}
}

func TestReverseApplication_DryRunNeedsNoVersionOrKeyAndAnswersEligibility(t *testing.T) {
	appID := ids.New()
	body := `{"applicationId":"` + appID + `","reason":"x","dryRun":true}`
	path := "/v1/receivables/payments/" + ids.New() + ":reverse-application"
	f := &fakeStore{eligibility: store.ReverseEligibility{Code: store.CodeReversalItemChanged, Reason: "touched", Items: []store.ItemChange{{OpenItemID: ids.New(), Cause: "DUNNED"}}}}
	h := router(f)
	if code := do(h, http.MethodPost, path, body, "receivables.payment.reverse", nil).Code; code != http.StatusForbidden {
		t.Errorf("dryRun without the duty: %d", code)
	}
	var got store.ReverseEligibility
	rec := do(h, http.MethodPost, path, body, "receivables.payment.post", nil)
	if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &got) != nil || got.Eligible || got.Code != store.CodeReversalItemChanged || len(got.Items) != 1 || f.lastSettlement != appID || f.lastVersion != 0 {
		t.Fatalf("dryRun: %d %s", rec.Code, rec.Body.String())
	}
	f.eligibility = store.ReverseEligibility{Eligible: true}
	rec = do(h, http.MethodPost, path, body, "receivables.payment.post", nil)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"eligible":true`) {
		t.Errorf("eligible dryRun: %d %s", rec.Code, rec.Body.String())
	}
}
