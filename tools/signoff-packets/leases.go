package main

import (
	"fmt"

	"github.com/shopspring/decimal"
)

const leasesSource = "services/assets/testdata/lease-25.json"

type leaseFile struct {
	Leases []struct {
		Name            string `json:"name"`
		Currency        string `json:"currency"`
		Payment         string `json:"payment"`
		PaymentsPerYear int    `json:"paymentsPerYear"`
		TermMonths      int    `json:"termMonths"`
		Timing          string `json:"timing"`
		IBRRate         string `json:"ibrRate"`
		RateMethod      string `json:"rateMethod"`
		Liability       string `json:"liability"`
		ROUAsset        string `json:"rouAsset"`
		TotalInterest   string `json:"totalInterest"`
		Lines           []struct {
			Period          int    `json:"period"`
			Opening         string `json:"opening"`
			Interest        string `json:"interest"`
			Payment         string `json:"payment"`
			Closing         string `json:"closing"`
			ROUDepreciation string `json:"rouDepreciation"`
		} `json:"lines"`
	} `json:"leases"`
}

func buildLeases(root string) ([]*Table, error) {
	var f leaseFile
	if err := readJSON(root, leasesSource, &f); err != nil {
		return nil, err
	}
	leases := newTable("Leases", "Inputs: the terms of each lease, and the expected initial liability, right-of-use asset and total interest",
		"Lease", "Currency", "Payment", "Payments per year", "Term (months)", "Timing", "Incremental borrowing rate", "Rate method",
		"Initial liability", "Right-of-use asset", "Total interest")
	schedule := newTable("Schedule", "Expected: the amortisation table of every lease, one row per payment period",
		"Lease", "Period", "Opening liability", "Interest", "Payment", "Closing liability", "Right-of-use depreciation")
	recon := newTable("Reconciliation", "Tie-out per lease: interest and depreciation add up, payments cover liability and interest, the liability ends at 0.00",
		"Lease", "Initial liability", "Payments", "Total interest", "Schedule interest", "Interest difference", "Payments less interest less liability",
		"Right-of-use asset", "Schedule depreciation", "Depreciation difference", "Closing liability at the end")
	for _, l := range f.Leases {
		leases.add(str(l.Name), str(l.Currency), num(l.Payment), integer(l.PaymentsPerYear), integer(l.TermMonths), str(l.Timing), num(l.IBRRate),
			str(l.RateMethod), num(l.Liability), num(l.ROUAsset), num(l.TotalInterest))
		interest, payments, deprec, last := decimal.Zero, decimal.Zero, decimal.Zero, decimal.Zero
		for _, ln := range l.Lines {
			schedule.add(str(l.Name), integer(ln.Period), num(ln.Opening), num(ln.Interest), num(ln.Payment), num(ln.Closing), num(ln.ROUDepreciation))
			vals := make([]decimal.Decimal, 4)
			for i, s := range []string{ln.Interest, ln.Payment, ln.ROUDepreciation, ln.Closing} {
				v, err := dec(s)
				if err != nil {
					return nil, fmt.Errorf("%s period %d: %w", l.Name, ln.Period, err)
				}
				vals[i] = v
			}
			interest, payments, deprec, last = interest.Add(vals[0]), payments.Add(vals[1]), deprec.Add(vals[2]), vals[3]
		}
		liability, err1 := dec(l.Liability)
		rou, err2 := dec(l.ROUAsset)
		total, err3 := dec(l.TotalInterest)
		if err1 != nil || err2 != nil || err3 != nil {
			return nil, fmt.Errorf("%s: bad liability, asset or interest", l.Name)
		}
		recon.add(str(l.Name), money(liability), money(payments), money(total), money(interest), money(total.Sub(interest)),
			money(payments.Sub(interest).Sub(liability)), money(rou), money(deprec), money(rou.Sub(deprec)), money(last))
	}
	return []*Table{leases, schedule, recon}, nil
}
