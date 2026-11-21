package store_test

import (
	"testing"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/testkit"
	"github.com/BenjaminSRussell/depot/services/intercompany/internal/domain"
)

func TestLinks_StoredOncePerSourceAndPerTenant(t *testing.T) {
	s, _, ctx := appSetup(t)
	other := testkit.Ctx(testkit.TenantID(2))
	l := domain.DocumentLink{PairID: "pair-1", SourceEntity: "A", SourceType: domain.DocSalesOrder, SourceID: "so-1", MirrorEntity: "B", MirrorType: domain.DocPurchaseOrder, MirrorID: "po-1"}
	first, created, err := s.CreateLink(ctx, l)
	if err != nil || !created || first.Status != domain.LinkLinked {
		t.Fatalf("create = %+v %v %v", first, created, err)
	}
	again, created, err := s.CreateLink(ctx, l)
	if err != nil || created || again.ID != first.ID {
		t.Fatalf("replay = %+v %v %v", again, created, err)
	}
	l.MirrorID = "po-2"
	if _, _, err := s.CreateLink(ctx, l); err == nil || err.(*domain.Error).Code != domain.CodeLinkConflict {
		t.Fatalf("another mirror for the same source = %v", err)
	}
	if got, _ := s.ListLinks(other, ""); len(got) != 0 {
		t.Fatalf("another tenant sees %d links", len(got))
	}
	done, err := s.CompensateLink(ctx, first.ID)
	if err != nil || done.Status != domain.LinkCompensated {
		t.Fatalf("compensate = %+v %v", done, err)
	}
	if _, err := s.CompensateLink(ctx, ids.New()); err == nil || err.(*domain.Error).Code != domain.CodeLinkNotFound {
		t.Fatalf("compensate missing = %v", err)
	}
	if _, err := s.CompensateLink(other, first.ID); err == nil {
		t.Fatal("another tenant must not compensate the link")
	}
}
