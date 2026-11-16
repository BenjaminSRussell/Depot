package api

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

func (f *fakeStore) ReportNodeReadings(context.Context, string, string, []store.NodeReadingIn) error {
	return nil
}
func (f *fakeStore) LatestNodeReading(_ context.Context, node string, _ time.Duration, now time.Time) (store.NodeReading, error) {
	return store.NodeReading{Node: node, ReadAt: now}, nil
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
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/node-readings?node=n1", "", DutyView); rec.Code != http.StatusForbidden {
		t.Fatalf("read with the view duty: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/node-readings?node=n1", "", DutyControlRecipeExecute); rec.Code != http.StatusOK {
		t.Fatalf("read: %d", rec.Code)
	}
	if rec := call(t, h, http.MethodGet, "/v1/shopfloor/node-readings", "", DutyControlRecipeExecute); rec.Code != http.StatusBadRequest {
		t.Fatalf("read without a node: %d", rec.Code)
	}
}

// The connector's readings reach the kiosk's readout: only the bound connector reports, the newest reading wins, and a
// node with no recent reading is SF-0196.
func TestNodeReadingsOnADatabase(t *testing.T) {
	e := newRecipeEnv(t)
	post := "/v1/shopfloor/edge-nodes/" + e.node.ID + "/readings"
	get := func(node string) (int, string) {
		rec := e.send(http.MethodGet, "/v1/shopfloor/node-readings?node="+node, "", DutyControlRecipeExecute, "operator-1")
		return rec.Code, rec.Body.String()
	}
	if code, body := get("ns%3D2%3Bs%3DOven1.Temp"); code != http.StatusNotFound || !strings.Contains(body, store.CodeNoReading) {
		t.Fatalf("no reading yet: %d %s", code, body)
	}
	now := time.Now().UTC()
	body := func(v string, at time.Time) string {
		return fmt.Sprintf(`{"readings":[{"node":"ns=2;s=Oven1.Temp","value":%q,"nodeType":"Double","at":%q}]}`, v, at.Format(time.RFC3339))
	}
	if rec := e.send(http.MethodPost, post, body("181.5", now.Add(-30*time.Second)), DutyEdgeConnect, "someone-else"); rec.Code == http.StatusNoContent {
		t.Fatalf("a caller that is not the bound connector reported readings")
	}
	if rec := e.send(http.MethodPost, post, body("181.5", now.Add(-30*time.Second)), DutyEdgeConnect, "edge-7"); rec.Code != http.StatusNoContent {
		t.Fatalf("report: %d %s", rec.Code, rec.Body)
	}
	if code, b := get("ns%3D2%3Bs%3DOven1.Temp"); code != http.StatusOK || !strings.Contains(b, `"value":"181.5"`) {
		t.Fatalf("read: %d %s", code, b)
	}
	// An older reading does not replace a newer one; a newer one does.
	e.send(http.MethodPost, post, body("100", now.Add(-10*time.Minute)), DutyEdgeConnect, "edge-7")
	if _, b := get("ns%3D2%3Bs%3DOven1.Temp"); !strings.Contains(b, `"value":"181.5"`) {
		t.Fatalf("an older reading replaced the newer one: %s", b)
	}
	e.send(http.MethodPost, post, body("182", now.Add(-5*time.Second)), DutyEdgeConnect, "edge-7")
	if _, b := get("ns%3D2%3Bs%3DOven1.Temp"); !strings.Contains(b, `"value":"182"`) {
		t.Fatalf("a newer reading did not replace: %s", b)
	}
	// A reading older than the maximum age answers nothing.
	e.send(http.MethodPost, "/v1/shopfloor/edge-nodes/"+e.node.ID+"/readings", fmt.Sprintf(`{"readings":[{"node":"stale-node","value":"1","at":%q}]}`, now.Add(-time.Hour).Format(time.RFC3339)), DutyEdgeConnect, "edge-7")
	if code, b := get("stale-node"); code != http.StatusNotFound || !strings.Contains(b, store.CodeNoReading) {
		t.Fatalf("a stale reading: %d %s", code, b)
	}
}
