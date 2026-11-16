package main

import (
	"strings"
)

const (
	ebicsSource     = "services/treasury/internal/ebics/testdata/returncodes.json"
	ebicsCodeSource = "services/treasury/internal/ebics/returncodes.go"
)

type returnCodeTable struct {
	UnlistedDisposition string `json:"unlistedDisposition"`
	Codes               []struct {
		AnnexName   string   `json:"annexName"`
		Constant    string   `json:"constant"`
		Code        string   `json:"code"`
		Disposition string   `json:"disposition"`
		Tests       []string `json:"tests"`
	} `json:"codes"`
}

var dispositionText = map[string]string{
	"proceed":  "carry on",
	"no-data":  "an empty download, not an error",
	"back-off": "wait and retry, up to the back-off limit",
	"park":     "stop and park the subscriber until a person unparks it",
}

func buildEBICS(root string) ([]*Table, error) {
	var t returnCodeTable
	if err := readJSON(root, ebicsSource, &t); err != nil {
		return nil, err
	}
	inputs := newTable("Return codes", "Inputs: the 13 return codes the client names, as the EBICS 2.5 annex lists them",
		"Annex name", "Code", "Constant in returncodes.go")
	expected := newTable("Dispositions", "Expected: what the client does on each code, the tests that cover it, and blank columns for the annex text and the verdict",
		"Annex name", "Code", "Disposition", "What the client does", "Covered by tests", "Spec annex text (reviewer)", "Agree? (reviewer)")
	counts := map[string]int{}
	for _, c := range t.Codes {
		inputs.add(str(c.AnnexName), str(c.Code), str(c.Constant))
		expected.add(str(c.AnnexName), str(c.Code), str(c.Disposition), str(dispositionText[c.Disposition]), str(strings.Join(c.Tests, ", ")), str(""), str(""))
		counts[c.Disposition]++
	}
	expected.add(str("Any code not listed"), str(""), str(t.UnlistedDisposition), str(dispositionText[t.UnlistedDisposition]),
		str("TestClassify_CodesMapToBackOffParkOrProceed, TestReturnCodes_ConstantsMatchTheEBICS25Annex"), str(""), str(""))
	recon := newTable("Reconciliation", "Tie-out: the codes per disposition add up to the 13 listed", "Disposition", "Codes")
	total := 0
	for _, d := range []string{"proceed", "no-data", "back-off", "park"} {
		recon.add(str(d), integer(counts[d]))
		total += counts[d]
	}
	recon.add(str("Total"), integer(total))
	return []*Table{inputs, expected, recon}, nil
}
