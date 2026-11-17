package main

import (
	"fmt"
	"sort"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/depot/platform/xlsx"
)

const (
	cashAppDir          = "services/receivables/testdata/cash-app-500"
	cashAppOpenItems    = cashAppDir + "/open_items.csv"
	cashAppAnswerKey    = cashAppDir + "/answer_key.csv"
	cashAppMatchResults = cashAppDir + "/match_outcomes.csv"
	// The payments the outcomes were matched from; listed as sources so the cover marks a local edit to them.
	cashAppLockbox     = cashAppDir + "/lockbox.bai2"
	cashAppStatement   = cashAppDir + "/statement.camt053.xml"
	cashAppRemittances = cashAppDir + "/remittances.json"
)

// cashAppWhy explains, per answer-key category, why the matcher does not auto-apply such a payment without a learned payer
// (BR-30.1 weights, auto-apply at 85; services/receivables/NOT-IMPLEMENTED.md, AC1).
var cashAppWhy = map[string]string{
	"WRONG_REFERENCE":   "Quotes another open invoice of the payer. The quoted invoice scores R1 60 + R6 5 at most, the one actually paid R2 30 + R6 5, both under 85.",
	"SHORT_NO_REASON":   "Pays less than the invoice and gives no reason, so R7 cannot apply: R1 60 + R6 5 at most.",
	"UNIDENTIFIED":      "No usable reference and no known payer: R2 30 + R6 5 at most.",
	"SHORT_WITH_REASON": "Short with a reason (R7), but the payer is not learned yet (no R4), so the score is 80, or 85 only with R6.",
	"SINGLE":            "One invoice paid in full with its reference.",
	"MULTI":             "Several invoices paid together with their references.",
	"DISCOUNT":          "Paid within the discount period, less the cash discount.",
}

func buildCashApp(root string) ([]*Table, error) {
	header, records, err := readCSV(root, cashAppOpenItems)
	if err != nil {
		return nil, err
	}
	oc, err := columns(cashAppOpenItems, header, "legal_entity_id", "customer_id", "document_number", "document_date", "due_date", "currency", "amount", "discount_date", "discount_amount")
	if err != nil {
		return nil, err
	}
	open := newTable("Open items", "Inputs: the open invoices the payments are matched against, decoy numbers and amounts included",
		"Legal entity", "Customer", "Document", "Document date", "Due date", "Currency", "Amount", "Discount date", "Discount amount")
	for _, r := range records {
		open.add(str(r[oc["legal_entity_id"]]), str(r[oc["customer_id"]]), str(r[oc["document_number"]]), str(r[oc["document_date"]]), str(r[oc["due_date"]]),
			str(r[oc["currency"]]), num(r[oc["amount"]]), str(r[oc["discount_date"]]), num(r[oc["discount_amount"]]))
	}

	header, records, err = readCSV(root, cashAppAnswerKey)
	if err != nil {
		return nil, err
	}
	ac, err := columns(cashAppAnswerKey, header, "payment_key", "source", "category", "documents")
	if err != nil {
		return nil, err
	}
	type answer struct{ source, category, documents string }
	key := map[string]answer{}
	answers := newTable("Answer key", "Inputs: the invoices each payment really pays, built with the payment and never by the matcher",
		"Payment", "Source", "Category", "Documents paid")
	for _, r := range records {
		a := answer{r[ac["source"]], r[ac["category"]], r[ac["documents"]]}
		key[r[ac["payment_key"]]] = a
		answers.add(str(r[ac["payment_key"]]), str(a.source), str(a.category), str(a.documents))
	}

	header, records, err = readCSV(root, cashAppMatchResults)
	if err != nil {
		return nil, err
	}
	mc, err := columns(cashAppMatchResults, header, "payment_key", "source", "currency", "amount", "value_date", "references", "short_pay_reason", "payer_known",
		"decision", "top_score", "top_rules", "top_documents")
	if err != nil {
		return nil, err
	}
	outcomes := newTable("Match outcomes", "Expected: the matcher's decision for each payment with no learned payers, against the answer key",
		"Payment", "Source", "Currency", "Amount", "Value date", "References", "Short-pay reason", "Payer known", "Decision", "Top score", "Rules",
		"Top candidate", "Category", "Documents paid", "Outcome")
	left := newTable("Not auto-applied", "Expected: the payments a person must apply, with why the rule leaves each one (the AC-30.1 decision)",
		"Payment", "Source", "Category", "Amount", "Decision", "Top score", "Rules", "Top candidate", "Documents paid", "Why it is not auto-applied")

	type tally struct {
		payments, correct, wrong, suggested, onAccount int
		amount                                         decimal.Decimal
	}
	byCat := map[string]*tally{}
	seen := map[string]bool{}
	for n, r := range records {
		pk := r[mc["payment_key"]]
		a, ok := key[pk]
		if !ok {
			return nil, fmt.Errorf("%s row %d: payment %s is not in the answer key", cashAppMatchResults, n+2, pk)
		}
		seen[pk] = true
		amount, err := dec(r[mc["amount"]])
		if err != nil {
			return nil, fmt.Errorf("%s row %d: %w", cashAppMatchResults, n+2, err)
		}
		t := byCat[a.category]
		if t == nil {
			t = &tally{}
			byCat[a.category] = t
		}
		t.payments++
		t.amount = t.amount.Add(amount)
		decision := r[mc["decision"]]
		outcome := "left for a person"
		switch decision {
		case "AUTO_APPLY":
			if sameDocs(r[mc["top_documents"]], a.documents) {
				outcome = "auto-applied correctly"
				t.correct++
			} else {
				outcome = "auto-applied to the wrong invoices"
				t.wrong++
			}
		case "SUGGESTED":
			t.suggested++
		default:
			t.onAccount++
		}
		row := []xlsx.Cell{str(pk), str(r[mc["source"]]), str(r[mc["currency"]]), money(amount), str(r[mc["value_date"]]), str(r[mc["references"]]),
			str(r[mc["short_pay_reason"]]), str(r[mc["payer_known"]]), str(decision), num(r[mc["top_score"]]), str(r[mc["top_rules"]]),
			str(r[mc["top_documents"]]), str(a.category), str(a.documents), str(outcome)}
		outcomes.add(row...)
		if decision != "AUTO_APPLY" {
			left.add(str(pk), str(r[mc["source"]]), str(a.category), money(amount), str(decision), num(r[mc["top_score"]]), str(r[mc["top_rules"]]),
				str(r[mc["top_documents"]]), str(a.documents), str(cashAppWhy[a.category]))
		}
	}
	if len(seen) != len(key) {
		return nil, fmt.Errorf("%s has %d payments, the answer key %d", cashAppMatchResults, len(seen), len(key))
	}

	recon := newTable("Reconciliation", "Tie-out per category: payments split into auto-applied, suggested and on account; the total is 500 and false matches are 0",
		"Category", "Payments", "Amount", "Auto-applied correctly", "Auto-applied wrongly", "Suggested", "On account")
	cats := make([]string, 0, len(byCat))
	for c := range byCat {
		cats = append(cats, c)
	}
	sort.Strings(cats)
	var all tally
	for _, c := range cats {
		t := byCat[c]
		recon.add(str(c), integer(t.payments), money(t.amount), integer(t.correct), integer(t.wrong), integer(t.suggested), integer(t.onAccount))
		all.payments, all.correct, all.wrong, all.suggested, all.onAccount = all.payments+t.payments, all.correct+t.correct, all.wrong+t.wrong,
			all.suggested+t.suggested, all.onAccount+t.onAccount
		all.amount = all.amount.Add(t.amount)
	}
	recon.add(str("Total"), integer(all.payments), money(all.amount), integer(all.correct), integer(all.wrong), integer(all.suggested), integer(all.onAccount))
	return []*Table{open, answers, outcomes, left, recon}, nil
}

func sameDocs(a, b string) bool {
	x, y := strings.Fields(a), strings.Fields(b)
	sort.Strings(x)
	sort.Strings(y)
	return strings.Join(x, " ") == strings.Join(y, " ")
}
