package store

import (
	"testing"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/services/compliance/internal/domain"
)

func TestAuditPolicies_CreateVersionRetireAndTenantScope(t *testing.T) {
	s, _ := migrated(t)
	tenantID := ids.New()
	ctx := as(tenantID, "auditor")
	in := AuditPolicyInput{Code: "GIFT-CARDS", Name: "Gift cards", Spec: domain.AuditSpec{Type: "KEYWORD", Keywords: []string{"gift card"}}}
	made, err := s.CreateAuditPolicy(ctx, in, "k1", t0)
	if err != nil || made.Version != 1 || !made.Active || made.RuleType != "KEYWORD" || len(made.Spec.Keywords) != 1 || made.CreatedBy != "auditor" {
		t.Fatalf("create = %+v, %v", made, err)
	}
	if again, err := s.CreateAuditPolicy(ctx, in, "k1", t0); err != nil || again.PolicyID != made.PolicyID {
		t.Fatalf("replay = %+v, %v", again, err)
	}
	if _, err := s.CreateAuditPolicy(ctx, in, "k2", t0); code(err) != CodeAuditPolicyDupe {
		t.Fatalf("duplicate code must be 409, got %v", err)
	}
	if made.RowVersion != 1 {
		t.Fatalf("row version = %d", made.RowVersion)
	}
	spec := domain.AuditSpec{Type: "THRESHOLD", Currency: "USD", Limits: map[string]decimal.Decimal{"MEALS": decimal.NewFromInt(200)}}
	if _, err := s.UpdateAuditPolicy(ctx, made.PolicyID, 7, AuditPolicyPatch{Spec: &spec}, t0); code(err) != CodeVersionConflict {
		t.Fatalf("stale version must be 412, got %v", err)
	}
	rule, err := s.UpdateAuditPolicy(ctx, made.PolicyID, 1, AuditPolicyPatch{Spec: &spec}, t0)
	if err != nil || rule.Version != 2 || rule.RuleType != "THRESHOLD" || !rule.Spec.Limits["MEALS"].Equal(decimal.NewFromInt(200)) {
		t.Fatalf("new spec = %+v, %v", rule, err)
	}
	renamed, off := "Spending limits", false
	named, err := s.UpdateAuditPolicy(ctx, made.PolicyID, 2, AuditPolicyPatch{Name: &renamed, Active: &off}, t0)
	if err != nil || named.Version != 2 || named.RowVersion != 3 || named.Name != renamed || named.Active {
		t.Fatalf("rename and retire keep the version: %+v, %v", named, err)
	}
	if list, err := s.ListAuditPolicies(ctx, AuditPolicyFilter{Q: "spending", Limit: 10}); err != nil || len(list) != 1 {
		t.Fatalf("search = %+v, %v", list, err)
	}
	if list, _ := s.ListAuditPolicies(ctx, AuditPolicyFilter{ActiveOnly: true, Limit: 10}); len(list) != 0 {
		t.Fatalf("retired policy listed as active: %+v", list)
	}
	if list, _ := s.ListAuditPolicies(ctx, AuditPolicyFilter{Code: "GIFT-CARDS", Limit: 10}); len(list) != 1 {
		t.Fatalf("exact code = %+v", list)
	}
	other := as(ids.New(), "auditor")
	if list, _ := s.ListAuditPolicies(other, AuditPolicyFilter{Limit: 10}); len(list) != 0 {
		t.Fatalf("another tenant sees %+v", list)
	}
	if _, err := s.GetAuditPolicy(other, made.PolicyID); code(err) != CodeAuditPolicyNotFound {
		t.Fatalf("cross-tenant get must be 404, got %v", err)
	}
}

func TestAuditPolicies_RegisterUnderAnExistingIdAndVersion(t *testing.T) {
	s, _ := migrated(t)
	ctx := as(ids.New(), "auditor")
	old := ids.New()
	made, err := s.CreateAuditPolicy(ctx, AuditPolicyInput{PolicyID: old, Version: 4, Code: "OLD", Name: "Old rule", Spec: domain.AuditSpec{Type: "DUPLICATE"}}, "k", t0)
	if err != nil || made.PolicyID != old || made.Version != 4 || made.RowVersion != 1 {
		t.Fatalf("registered = %+v, %v", made, err)
	}
	if _, err := s.CreateAuditPolicy(ctx, AuditPolicyInput{PolicyID: old, Code: "OTHER", Name: "Same id", Spec: domain.AuditSpec{Type: "DUPLICATE"}}, "k2", t0); err == nil {
		t.Fatal("a second policy under the same id must be refused")
	}
}
