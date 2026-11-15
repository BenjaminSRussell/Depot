package upstream

import (
	"context"
	"net/http"
	"testing"
)

func TestDecodePriced(t *testing.T) {
	ok := []byte(`{"lines":[{"lineNo":1,"unit":"EA","unitPrice":"100.00","netUnitPrice":"87.5000"},{"lineNo":2,"unit":"EA","errorCode":"PRC-0001"}]}`)
	got, err := decodePriced(http.StatusOK, ok, 2)
	if err != nil || len(got) != 2 || got[0].NetUnitPrice.String() != "87.5" || got[0].ErrorCode != "" || got[1].ErrorCode != "PRC-0001" || !got[1].NetUnitPrice.IsZero() {
		t.Fatalf("%+v %v", got, err)
	}
	if _, err := decodePriced(http.StatusOK, ok, 3); err == nil {
		t.Error("a short answer must be refused")
	}
	if _, err := decodePriced(http.StatusOK, []byte(`{"lines":[{"lineNo":1,"unit":"EA","unitPrice":"x","netUnitPrice":"1"}]}`), 1); err == nil {
		t.Error("a bad price must be refused")
	}
	if _, err := decodePriced(http.StatusForbidden, nil, 1); err == nil {
		t.Error("a refusal must be an error")
	}
	if _, err := New("", "", nil).ResolvePrices(context.Background(), PriceRequest{}); err != ErrNotConfigured {
		t.Errorf("unconfigured: %v", err)
	}
}
