package api

import (
	"context"
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/services/shopfloor/internal/store"
)

func (f *fakeStore) ReportNodeReadings(context.Context, string, string, []store.NodeReadingIn) error {
	return nil
}
func (f *fakeStore) ClaimReadout(_ context.Context, _, key string, _ time.Duration, now time.Time) (store.NodeReading, error) {
	return store.NodeReading{Node: key, ReadAt: now}, nil
}
func (f *fakeStore) PIStepData(context.Context, string) (store.PIStepData, error) {
	return store.PIStepData{}, httpx.NewProblem(http.StatusNotFound, store.CodeRecipeNotFound, "control recipe not found")
}

func TestNodeReadingRoutes_DutiesAndValidation(t *testing.T) {
	h := newRouter(&fakeStore{})
	post := "/v1/shopfloor/edge-nodes/" + edgeID + "/readings"
	ok := `{"readings":[{"node":"ns=2;s=Oven1.Temp","value":"181.5","nodeType":"Double"}]}`
	if rec := call(t, h, http.MethodPost, post, ok, DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("report without the connect duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, post, ok, DutyEdgeConnect); rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", rec.Code, rec.Body)
	}
	for name, body := range map[string]string{"empty": `{"readings":[]}`, "no node": `{"readings":[{"node":" ","value":"1"}]}`, "bad type": `{"readings":[{"node":"n","value":"1","nodeType":"Text"}]}`} {
		if rec := call(t, h, http.MethodPost, post, body, DutyEdgeConnect); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d", name, rec.Code)
		}
	}
	claim := "/v1/shopfloor/control-recipes/" + edgeID + "/readouts"
	if rec := call(t, h, http.MethodPost, claim, `{"stepKey":"U/O/P/1"}`, DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("claim with the view duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, claim, `{"stepKey":"U/O/P/1"}`, DutyControlRecipeExecute); rec.Code != http.StatusOK {
		t.Fatalf("claim: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodPost, claim, `{}`, DutyControlRecipeExecute); rec.Code != http.StatusBadRequest {
		t.Fatalf("claim without a step: %d", rec.Code)
	}
	future := fmt.Sprintf(`{"readings":[{"node":"n","value":"1","at":%q}]}`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if rec := call(t, h, http.MethodPost, post, future, DutyEdgeConnect); rec.Code != http.StatusBadRequest {
		t.Fatalf("a reading from the future: %d", rec.Code)
	}
}
