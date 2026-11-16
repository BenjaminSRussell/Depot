package store

import (
	"testing"

	"github.com/BenjaminSRussell/passage/platform/testkit"
)

// The PI category master through Postgres as passage_app: a code is unique per tenant (SF-0142), a change needs the
// version read (SF-0143), and another tenant sees none of it.
func TestPICategory_MasterVersionedAndTenantScoped(t *testing.T) {
	s, _, _ := setup(t)
	ctx := testkit.Ctx(testkit.TenantID(5901))
	other := testkit.Ctx(testkit.TenantID(5902))
	weigh, err := s.CreatePICategory(ctx, PICategoryInput{Code: "WEIGH", Name: "Weighing", Description: "Scale steps", Active: true}, "eng", Idem{Key: "pc1", Fingerprint: "pc1"})
	if err != nil || weigh.Version != 1 || weigh.Description != "Scale steps" {
		t.Fatalf("create = %+v, %v", weigh, err)
	}
	if _, err := s.CreatePICategory(ctx, PICategoryInput{Code: "WEIGH", Name: "Again", Active: true}, "eng", Idem{Key: "pc2", Fingerprint: "pc2"}); problemCode(err) != CodeSetupCodeTaken {
		t.Fatalf("duplicate code = %v, want %s", err, CodeSetupCodeTaken)
	}
	if _, err := s.CreatePICategory(other, PICategoryInput{Code: "WEIGH", Name: "Other tenant", Active: true}, "eng", Idem{Key: "pc3", Fingerprint: "pc3"}); err != nil {
		t.Fatalf("the same code in another tenant: %v", err)
	}
	if _, err := s.UpdatePICategory(ctx, weigh.ID, 9, PICategoryInput{Name: "Weighing"}, "eng"); problemCode(err) != CodeVersionMismatch {
		t.Fatalf("stale version = %v, want %s", err, CodeVersionMismatch)
	}
	off, err := s.UpdatePICategory(ctx, weigh.ID, 1, PICategoryInput{Name: "Weighing and dosing", Active: false}, "eng")
	if err != nil || off.Version != 2 || off.Active || off.Code != "WEIGH" || off.Name != "Weighing and dosing" {
		t.Fatalf("update = %+v, %v", off, err)
	}
	if _, err := s.CreatePICategory(ctx, PICategoryInput{Code: "CLEAN", Name: "Cleaning", Active: true}, "eng", Idem{Key: "pc4", Fingerprint: "pc4"}); err != nil {
		t.Fatal(err)
	}
	all, err := s.ListPICategories(ctx, false, 10)
	if err != nil || len(all) != 2 || all[0].Code != "CLEAN" {
		t.Fatalf("list = %+v, %v", all, err)
	}
	active, err := s.ListPICategories(ctx, true, 10)
	if err != nil || len(active) != 1 || active[0].Code != "CLEAN" {
		t.Fatalf("active list = %+v, %v", active, err)
	}
	if _, err := s.GetPICategory(other, weigh.ID); problemCode(err) != CodePICategoryNotFound {
		t.Fatalf("another tenant reads it: %v", err)
	}
}
