package api_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/api"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/domain"
)

type fakeLinks struct {
	bySource map[string]domain.DocumentLink
}

func newFakeLinks() *fakeLinks { return &fakeLinks{bySource: map[string]domain.DocumentLink{}} }

func (f *fakeLinks) CreateLink(_ context.Context, l domain.DocumentLink) (domain.DocumentLink, bool, error) {
	if err := l.Validate(); err != nil {
		return domain.DocumentLink{}, false, err
	}
	key := l.SourceType + "/" + l.SourceID
	if have, ok := f.bySource[key]; ok {
		if !have.SameDocuments(l) {
			return domain.DocumentLink{}, false, &domain.Error{Code: domain.CodeLinkConflict, Detail: "already linked"}
		}
		return have, false, nil
	}
	l.ID, l.Status, l.CreatedBy = ids.New(), domain.LinkLinked, "user-7"
	f.bySource[key] = l
	return l, true, nil
}

func (f *fakeLinks) ListLinks(_ context.Context, pairID string) ([]domain.DocumentLink, error) {
	out := []domain.DocumentLink{}
	for _, l := range f.bySource {
		if pairID == "" || l.PairID == pairID {
			out = append(out, l)
		}
	}
	return out, nil
}

func (f *fakeLinks) CompensateLink(_ context.Context, id string) (domain.DocumentLink, error) {
	for k, l := range f.bySource {
		if l.ID == id {
			l.Status = domain.LinkCompensated
			f.bySource[k] = l
			return l, nil
		}
	}
	return domain.DocumentLink{}, &domain.Error{Code: domain.CodeLinkNotFound, Detail: "none"}
}

const linksPath = "/v1/interco/links"

func linkJSON(sourceType, sourceID, mirrorType, mirrorID string) string {
	return `{"pairId":"pair-1","sourceEntity":"A","sourceType":"` + sourceType + `","sourceId":"` + sourceID + `","mirrorEntity":"B","mirrorType":"` + mirrorType + `","mirrorId":"` + mirrorID + `","pricingBasis":"CostPlus"}`
}

func linkCall(method, path, body string) call {
	return call{method: method, path: path, duties: postDuties, body: body}
}

func TestLinks_MirrorMatrixStoredOnceAndCompensated(t *testing.T) {
	h := router(api.New(nil).WithLinks(newFakeLinks()))
	for _, c := range []struct{ source, mirror string }{
		{domain.DocSalesOrder, domain.DocPurchaseOrder},
		{domain.DocPackingSlip, domain.DocReceipt},
		{domain.DocInvoice, domain.DocVendorInvoice},
	} {
		rec := linkCall(http.MethodPost, linksPath, linkJSON(c.source, "src-"+c.source, c.mirror, "mir-"+c.source)).on(h)
		if rec.Code != http.StatusCreated {
			t.Fatalf("%s link = %d %s", c.source, rec.Code, rec.Body.String())
		}
	}
	replay := linkCall(http.MethodPost, linksPath, linkJSON(domain.DocSalesOrder, "src-sales_order", domain.DocPurchaseOrder, "mir-sales_order")).on(h)
	if replay.Code != http.StatusOK {
		t.Fatalf("replay = %d %s", replay.Code, replay.Body.String())
	}
	var list struct {
		Items []struct {
			ID, Status string
		} `json:"items"`
	}
	rec := linkCall(http.MethodGet, linksPath+"?pairId=pair-1", "").on(h)
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil || len(list.Items) != 3 {
		t.Fatalf("list = %d %s", rec.Code, rec.Body.String())
	}
	comp := linkCall(http.MethodPost, linksPath+"/"+list.Items[0].ID+"/compensate", "").on(h)
	if comp.Code != http.StatusOK || !strings.Contains(comp.Body.String(), `"status":"compensated"`) {
		t.Fatalf("compensate = %d %s", comp.Code, comp.Body.String())
	}
}

func TestLinks_Refusals(t *testing.T) {
	h := router(api.New(nil).WithLinks(newFakeLinks()))
	if rec := linkCall(http.MethodPost, linksPath, linkJSON(domain.DocSalesOrder, "so-1", domain.DocPurchaseOrder, "po-1")).on(h); rec.Code != http.StatusCreated {
		t.Fatalf("seed = %d", rec.Code)
	}
	for name, c := range map[string]struct {
		body string
		want int
	}{
		"another mirror for the same source": {linkJSON(domain.DocSalesOrder, "so-1", domain.DocPurchaseOrder, "po-2"), http.StatusConflict},
		"mirror type off the matrix":         {linkJSON(domain.DocSalesOrder, "so-3", domain.DocReceipt, "r-1"), http.StatusBadRequest},
		"unknown source type":                {linkJSON("rma", "x-1", domain.DocReceipt, "r-2"), http.StatusBadRequest},
		"same entity on both sides":          {strings.Replace(linkJSON(domain.DocSalesOrder, "so-4", domain.DocPurchaseOrder, "po-4"), `"mirrorEntity":"B"`, `"mirrorEntity":"A"`, 1), http.StatusBadRequest},
		"empty mirror id":                    {linkJSON(domain.DocSalesOrder, "so-5", domain.DocPurchaseOrder, " "), http.StatusBadRequest},
	} {
		if rec := linkCall(http.MethodPost, linksPath, c.body).on(h); rec.Code != c.want {
			t.Errorf("%s = %d %s, want %d", name, rec.Code, rec.Body.String(), c.want)
		}
	}
	if rec := linkCall(http.MethodPost, linksPath+"/not-an-id/compensate", "").on(h); rec.Code != http.StatusNotFound {
		t.Errorf("compensate unknown id = %d", rec.Code)
	}
	if rec := linkCall(http.MethodPost, linksPath+"/"+ids.New()+"/compensate", "").on(h); rec.Code != http.StatusNotFound {
		t.Errorf("compensate missing link = %d", rec.Code)
	}
}

func TestLinks_AuthAndConfiguration(t *testing.T) {
	h := router(api.New(nil).WithLinks(newFakeLinks()))
	noDuty := call{method: http.MethodPost, path: linksPath, duties: api.DutyView, body: linkJSON(domain.DocSalesOrder, "so-1", domain.DocPurchaseOrder, "po-1")}
	if rec := noDuty.on(h); rec.Code != http.StatusForbidden {
		t.Errorf("create without intercompany.post = %d", rec.Code)
	}
	noTenant := call{method: http.MethodGet, path: linksPath, duties: api.DutyView, tenant: "not-a-tenant"}
	if rec := noTenant.on(h); rec.Code != http.StatusUnauthorized {
		t.Errorf("malformed tenant = %d", rec.Code)
	}
	unconfigured := router(api.New(nil))
	if rec := linkCall(http.MethodGet, linksPath, "").on(unconfigured); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("unconfigured store = %d", rec.Code)
	}
}
