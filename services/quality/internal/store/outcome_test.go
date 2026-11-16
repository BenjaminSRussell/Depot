package store

import (
	"net/http"
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
	"github.com/BenjaminSRussell/passage/services/quality/internal/domain"
)

func TestOutcomeMaster_CreateSearchRetireAndTenantScope(t *testing.T) {
	s, _, ctx := setup(t)
	in, err := domain.NewOutcome("no-defect", "No visible defect")
	if err != nil {
		t.Fatal(err)
	}
	made, inserted, err := s.CreateOutcome(ctx, in, "k1")
	if err != nil || !inserted || made.Code != "NO-DEFECT" || !made.Active || made.Version != 1 {
		t.Fatalf("create = %+v inserted %v, %v", made, inserted, err)
	}
	if again, inserted, err := s.CreateOutcome(ctx, in, "k1"); err != nil || inserted || again.ID != made.ID {
		t.Fatalf("replay = %+v inserted %v, %v", again, inserted, err)
	}
	if _, _, err := s.CreateOutcome(ctx, in, "k2"); problemStatus(err) != http.StatusConflict {
		t.Fatalf("duplicate code must be 409, got %v", err)
	}
	scratch, _ := domain.NewOutcome("SCRATCH", "Surface scratch")
	if _, _, err := s.CreateOutcome(ctx, scratch, ""); err != nil {
		t.Fatal(err)
	}
	if found, err := s.ListOutcomes(ctx, OutcomeFilter{Q: "visible", Limit: 10}); err != nil || len(found) != 1 || found[0].ID != made.ID {
		t.Fatalf("search by name = %+v, %v", found, err)
	}
	if found, err := s.ListOutcomes(ctx, OutcomeFilter{Code: "SCRATCH", Limit: 10}); err != nil || len(found) != 1 || found[0].Name != "Surface scratch" {
		t.Fatalf("exact code = %+v, %v", found, err)
	}
	renamed := "Clean surface"
	off := false
	if _, err := s.UpdateOutcome(ctx, made.ID, 5, &renamed, nil); problemStatus(err) != http.StatusPreconditionFailed {
		t.Fatalf("stale version must be 412, got %v", err)
	}
	up, err := s.UpdateOutcome(ctx, made.ID, 1, &renamed, &off)
	if err != nil || up.Version != 2 || up.Name != renamed || up.Active {
		t.Fatalf("update = %+v, %v", up, err)
	}
	if active, _ := s.ListOutcomes(ctx, OutcomeFilter{ActiveOnly: true, Limit: 10}); len(active) != 1 || active[0].Code != "SCRATCH" {
		t.Fatalf("active only = %+v", active)
	}
	codes, err := s.OutcomeIDsByCode(ctx, []string{"NO-DEFECT", "SCRATCH", "NOPE"})
	if err != nil || len(codes) != 1 || codes["SCRATCH"] == "" {
		t.Fatalf("by code = %+v, %v (a retired outcome is not resolvable)", codes, err)
	}
	other := testkit.Ctx(testkit.TenantID(2), "quality.view")
	if list, _ := s.ListOutcomes(other, OutcomeFilter{Limit: 10}); len(list) != 0 {
		t.Fatalf("another tenant sees %+v", list)
	}
	if _, err := s.GetOutcome(other, made.ID); problemStatus(err) != http.StatusNotFound {
		t.Fatalf("cross-tenant get must be 404, got %v", err)
	}
}

func TestOutcomeMaster_ResolvesCodesAndIdsButNotRetiredCodes(t *testing.T) {
	s, _, ctx := setup(t)
	a, _ := domain.NewOutcome("PASS", "Pass")
	madeA, _, _ := s.CreateOutcome(ctx, a, "")
	b, _ := domain.NewOutcome("OLD", "Old")
	madeB, _, _ := s.CreateOutcome(ctx, b, "")
	off := false
	if _, err := s.UpdateOutcome(ctx, madeB.ID, 1, nil, &off); err != nil {
		t.Fatal(err)
	}
	got, err := s.ResolveOutcomeRefs(ctx, []string{"pass", madeB.ID, "OLD", "NOPE"})
	if err != nil || got["pass"] != madeA.ID || got[madeB.ID] != madeB.ID || len(got) != 2 {
		t.Fatalf("resolved = %+v, %v (a retired outcome resolves by id only)", got, err)
	}
}
