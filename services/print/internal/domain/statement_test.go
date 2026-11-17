package domain_test

import (
	"strings"
	"testing"
	"time"

	"github.com/BenjaminSRussell/depot/services/print/internal/domain"
)

func day(s string) time.Time {
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestNextRunOn(t *testing.T) {
	for _, c := range []struct {
		cadence string
		runDay  int
		after   string
		want    string
	}{
		{domain.CadenceMonthly, 5, "2026-11-15", "2026-12-05"},
		{domain.CadenceMonthly, 15, "2026-11-15", "2026-12-15"}, // strictly after
		{domain.CadenceMonthly, 28, "2026-11-15", "2026-11-28"},
		{domain.CadenceMonthly, 1, "2026-12-31", "2027-01-01"},
		{domain.CadenceMonthly, 28, "2027-01-28", "2027-02-28"},
		{domain.CadenceWeekly, 1, "2026-11-15", "2026-11-16"}, // 15 Nov 2026 is a Sunday; Monday is the 16th
		{domain.CadenceWeekly, 7, "2026-11-15", "2026-11-22"},
		{domain.CadenceWeekly, 5, "2026-11-13", "2026-11-20"}, // Friday after a Friday is the next week's
	} {
		if got := domain.NextRunOn(c.cadence, c.runDay, day(c.after)).Format(time.DateOnly); got != c.want {
			t.Errorf("%s day %d after %s = %s, want %s", c.cadence, c.runDay, c.after, got, c.want)
		}
	}
}

func TestStatementSchedule_Validate(t *testing.T) {
	ok := domain.StatementSchedule{Name: "Monthly", LegalEntityID: "0190a5f2-7b1c-7d3e-8a4f-123456789a01", SenderName: "Cedar Ridge Pumps", Cadence: domain.CadenceMonthly, RunDay: 3, Language: "en"}
	if f := ok.Validate(); len(f) != 0 {
		t.Fatalf("valid schedule: %v", f)
	}
	for field, mutate := range map[string]func(*domain.StatementSchedule){
		"name":          func(s *domain.StatementSchedule) { s.Name = " " },
		"senderName":    func(s *domain.StatementSchedule) { s.SenderName = "" },
		"legalEntityId": func(s *domain.StatementSchedule) { s.LegalEntityID = "x" },
		"cadence":       func(s *domain.StatementSchedule) { s.Cadence = "DAILY" },
		"runDay":        func(s *domain.StatementSchedule) { s.RunDay = 29 },
		"language":      func(s *domain.StatementSchedule) { s.Language = "english" },
		"templateCode":  func(s *domain.StatementSchedule) { s.TemplateCode = "bad code" },
	} {
		s := ok
		mutate(&s)
		if f := s.Validate(); f[field] == "" {
			t.Errorf("%s was not refused: %v", field, f)
		}
	}
	weekly := ok
	weekly.Cadence, weekly.RunDay = domain.CadenceWeekly, 8
	if f := weekly.Validate(); f["runDay"] == "" {
		t.Errorf("weekday 8: %v", f)
	}
}

func TestStatementDocument_RendersAndCutsLongStatements(t *testing.T) {
	due := "2026-10-31"
	sections := []domain.StatementSection{{Currency: "USD", ClosingBalance: "150.00", Lines: []domain.StatementLine{
		{SourceType: "INVOICE", DocumentNumber: "INV-1\t7", DocumentDate: "2026-10-01", DueDate: &due, Amount: "150.00", OpenAmount: "150.00", RunningBalance: "150.00"},
	}}}
	doc := domain.StatementDocument("Cedar Ridge Pumps, Inc.", "Bluewater Utilities", "2026-11-15", sections)
	if doc.DocumentType != domain.DocPeriodicStatement || doc.Counterparty == nil || doc.Counterparty.Name != "Bluewater Utilities" ||
		doc.Lines[0][1] != "INV-1 7" || doc.Lines[0][2] != "Invoice" || !strings.Contains(doc.Body, "150.00 USD") {
		t.Fatalf("document: %+v", doc)
	}
	if _, err := domain.RenderDocumentReport(doc); err != nil {
		t.Fatalf("the statement does not render: %v", err)
	}
	many := domain.StatementSection{Currency: "USD", ClosingBalance: "1.00"}
	for i := 0; i < domain.MaxStatementLines+5; i++ {
		many.Lines = append(many.Lines, domain.StatementLine{SourceType: "INVOICE", DocumentNumber: "X", DocumentDate: "2026-10-01", Amount: "1", OpenAmount: "1", RunningBalance: "1"})
	}
	long := domain.StatementDocument("Cedar Ridge Pumps", "", "2026-11-15", []domain.StatementSection{many})
	if len(long.Lines) != domain.MaxStatementLines || !strings.Contains(long.Notes, "first 500 of 505") || long.Counterparty != nil {
		t.Fatalf("long statement: %d lines, notes %q", len(long.Lines), long.Notes)
	}
	if _, err := domain.RenderDocumentReport(long); err != nil {
		t.Fatalf("the long statement does not render: %v", err)
	}
	empty := domain.StatementDocument("Cedar Ridge Pumps", "Bluewater", "2026-11-15", nil)
	if !strings.Contains(empty.Body, "Nothing is open") {
		t.Fatalf("empty body: %q", empty.Body)
	}
}
