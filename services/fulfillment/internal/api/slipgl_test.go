package api

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/fulfillment/internal/store"
)

type slipGLCalls struct {
	released string
	idem     store.Idem
	err      error
}

func (f *fakeStore) ReleaseSlipGL(_ context.Context, slipID string, idem store.Idem) (store.SlipGL, error) {
	f.slipGL.released, f.slipGL.idem = slipID, idem
	if f.slipGL.err != nil {
		return store.SlipGL{}, f.slipGL.err
	}
	return store.SlipGL{SlipID: slipID, Status: "PENDING"}, nil
}

func TestSlipGL_ReleaseRoute(t *testing.T) {
	fs := &fakeStore{}
	h := newServer(t, fs)
	id := ids.New()
	path := "/v1/fulfillment/packing-slips/" + id + ":release-gl"

	if rec := do(t, h, http.MethodPost, path, "", nil); rec.Code != http.StatusBadRequest {
		t.Fatalf("release without a key: %d", rec.Code)
	}
	rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "rel-1"})
	if rec.Code != http.StatusOK || fs.slipGL.released != id || fs.slipGL.idem.Key != "rel-1" || !strings.Contains(rec.Body.String(), `"parked":false`) {
		t.Fatalf("release: %d %s", rec.Code, rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/v1/fulfillment/packing-slips/nope:release-gl", "", map[string]string{"Idempotency-Key": "rel-2"}); rec.Code != http.StatusNotFound {
		t.Fatalf("bad id: %d", rec.Code)
	}
	if rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "rel-3", "X-Depot-Duties": "fulfillment.packing-slip.post"}); rec.Code != http.StatusForbidden {
		t.Fatalf("release with only fulfillment.packing-slip.post: %d", rec.Code)
	}
	for status, code := range map[int]string{http.StatusNotFound: store.CodeSlipGLNotFound, http.StatusConflict: store.CodeSlipGLState} {
		fs.slipGL.err = httpx.NewProblem(status, code, "refused")
		if rec := do(t, h, http.MethodPost, path, "", map[string]string{"Idempotency-Key": "rel-4"}); rec.Code != status || !strings.Contains(rec.Body.String(), code) {
			t.Fatalf("store problem %s: %d %s", code, rec.Code, rec.Body.String())
		}
	}
}
