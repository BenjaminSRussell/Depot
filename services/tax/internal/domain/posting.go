package domain

import "fmt"

const CodeNoPostingAccount = "TAX-0015"

type PostingLine struct {
	Account      string `json:"account"`
	Credit       bool   `json:"credit"`
	Amount       Dec    `json:"amount"`
	TaxCode      string `json:"tax_code"`
	Jurisdiction string `json:"jurisdiction"`
}

func PostingLines(res Result, accounts map[string]string) ([]PostingLine, []Warning) {
	lines := []PostingLine{}
	warnings := []Warning{}
	index := map[PostingLine]int{}
	missing := map[string]bool{}
	for _, l := range res.Lines {
		for _, t := range l.Taxes {
			key := MovementKey(t.Code, t.Direction)
			account := accounts[key]
			if account == "" {
				if !missing[key] {
					missing[key] = true
					warnings = append(warnings, Warning{Code: CodeNoPostingAccount, Detail: fmt.Sprintf("no GL account is configured for %s, so its tax has no posting line", key)})
				}
				continue
			}
			amount, credit := t.TaxAmount.Decimal, t.Direction != DirInput
			if t.Direction == DirInput {
				amount = t.Recoverable.Decimal
			}
			if amount.IsNegative() {
				amount, credit = amount.Neg(), !credit
			}
			if amount.IsZero() {
				continue
			}
			bucket := PostingLine{Account: account, Credit: credit, TaxCode: t.Code, Jurisdiction: t.Jurisdiction}
			i, ok := index[bucket]
			if !ok {
				i = len(lines)
				index[bucket] = i
				lines = append(lines, bucket)
			}
			lines[i].Amount = Dec{lines[i].Amount.Add(amount)}
		}
	}
	return lines, warnings
}
