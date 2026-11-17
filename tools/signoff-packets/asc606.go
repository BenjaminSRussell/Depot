package main

import (
	"fmt"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/xlsx"
)

const asc606Source = "services/revrec/internal/domain/testdata/asc606-40.json"

type asc606File struct {
	Seed      int `json:"seed"`
	Scenarios []struct {
		ID               string `json:"id"`
		Description      string `json:"description"`
		Currency         string `json:"currency"`
		TransactionPrice string `json:"transactionPrice"`
		ResidualRule     string `json:"residualRule"`
		RatableBasis     string `json:"ratableBasis"`
		ContractDate     string `json:"contractDate"`
		AsOf             string `json:"asOf"`
		Obligations      []struct {
			ID          string `json:"id"`
			SSP         string `json:"ssp"`
			SSPSource   string `json:"sspSource"`
			Method      string `json:"method"`
			SatisfiedOn string `json:"satisfiedOn"`
			TermStart   string `json:"termStart"`
			TermEnd     string `json:"termEnd"`
		} `json:"obligations"`
		Expected struct {
			Rejected       string   `json:"rejected"`
			Allocated      []string `json:"allocated"`
			ClosingByMonth []struct {
				Month   string `json:"month"`
				Closing string `json:"closing"`
			} `json:"closingByMonth"`
			Schedules [][]struct {
				Date   *string `json:"date"`
				Amount string  `json:"amount"`
			} `json:"schedules"`
			RecognizedAsOf    string `json:"recognizedAsOf"`
			DeferredAdditions string `json:"deferredAdditions"`
			DeferredAsOf      string `json:"deferredAsOf"`
		} `json:"expected"`
	} `json:"scenarios"`
}

func buildASC606(root string) ([]*Table, error) {
	var f asc606File
	if err := readJSON(root, asc606Source, &f); err != nil {
		return nil, err
	}
	scenarios := newTable("Scenarios", "Inputs: each contract (price, residual rule, ratable basis, dates) and the refusal expected, if any",
		"Scenario", "Description", "Currency", "Transaction price", "Residual rule", "Ratable basis", "Contract date", "As of", "Expected refusal")
	obligations := newTable("Obligations", "Inputs and expected: each performance obligation with its SSP and method, and the price allocated to it",
		"Scenario", "Obligation", "SSP", "SSP source", "Method", "Satisfied on", "Term start", "Term end", "Allocated (expected)")
	schedules := newTable("Schedules", "Expected: the revenue schedule of every obligation (a milestone without a date waits for its milestone)",
		"Scenario", "Obligation", "Date", "Amount")
	deferred := newTable("Deferred by month", "Expected: the deferred revenue balance at each month end up to the as-of date",
		"Scenario", "Month", "Closing deferred revenue")
	recon := newTable("Reconciliation", "Tie-out per scenario: allocation and schedules add up to the price, and recognized plus deferred is the price at the as-of date",
		"Scenario", "Transaction price", "Allocated", "Allocation difference", "Scheduled", "Schedule difference", "Recognized as of", "Deferred as of",
		"Recognized plus deferred less price", "Deferred additions", "Expected refusal")
	for _, s := range f.Scenarios {
		e := s.Expected
		scenarios.add(str(s.ID), str(s.Description), str(s.Currency), num(s.TransactionPrice), str(s.ResidualRule), str(s.RatableBasis), str(s.ContractDate),
			str(s.AsOf), str(e.Rejected))
		price, err := dec(s.TransactionPrice)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", s.ID, err)
		}
		if e.Rejected != "" {
			for _, o := range s.Obligations {
				obligations.add(str(s.ID), str(o.ID), num(o.SSP), str(o.SSPSource), str(o.Method), str(o.SatisfiedOn), str(o.TermStart), str(o.TermEnd), xlsx.Cell{})
			}
			recon.add(str(s.ID), money(price), str(""), str(""), str(""), str(""), str(""), str(""), str(""), str(""), str(e.Rejected))
			continue
		}
		if len(e.Allocated) != len(s.Obligations) || len(e.Schedules) != len(s.Obligations) {
			return nil, fmt.Errorf("%s: %d obligations, %d allocations, %d schedules", s.ID, len(s.Obligations), len(e.Allocated), len(e.Schedules))
		}
		allocated, scheduled := decimal.Zero, decimal.Zero
		for i, o := range s.Obligations {
			obligations.add(str(s.ID), str(o.ID), num(o.SSP), str(o.SSPSource), str(o.Method), str(o.SatisfiedOn), str(o.TermStart), str(o.TermEnd), num(e.Allocated[i]))
			a, err := dec(e.Allocated[i])
			if err != nil {
				return nil, fmt.Errorf("%s %s: %w", s.ID, o.ID, err)
			}
			allocated = allocated.Add(a)
			for _, l := range e.Schedules[i] {
				date := ""
				if l.Date != nil {
					date = *l.Date
				}
				schedules.add(str(s.ID), str(o.ID), str(date), num(l.Amount))
				v, err := dec(l.Amount)
				if err != nil {
					return nil, fmt.Errorf("%s %s: %w", s.ID, o.ID, err)
				}
				scheduled = scheduled.Add(v)
			}
		}
		for _, m := range e.ClosingByMonth {
			deferred.add(str(s.ID), str(m.Month), num(m.Closing))
		}
		rec, err1 := dec(e.RecognizedAsOf)
		def, err2 := dec(e.DeferredAsOf)
		if err1 != nil || err2 != nil {
			return nil, fmt.Errorf("%s: bad recognized or deferred amount", s.ID)
		}
		recon.add(str(s.ID), money(price), money(allocated), money(allocated.Sub(price)), money(scheduled), money(scheduled.Sub(price)), money(rec), money(def),
			money(rec.Add(def).Sub(price)), num(e.DeferredAdditions), str(""))
	}
	return []*Table{scenarios, obligations, schedules, deferred, recon}, nil
}
