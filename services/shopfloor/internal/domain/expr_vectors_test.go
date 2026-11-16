package domain

import (
	"encoding/json"
	"os"
	"testing"

	"github.com/shopspring/decimal"
)

type exprVectors struct {
	Vars  map[string]string `json:"vars"`
	Cases []struct {
		Expr string `json:"expr"`
		Want string `json:"want"`
	} `json:"cases"`
	Refuse []string `json:"refuse"`
}

// The vectors are shared with the kiosk's evaluator: both must agree on every one.
func TestExprSharedVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/expr_vectors.json")
	if err != nil {
		t.Fatal(err)
	}
	var v exprVectors
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatal(err)
	}
	vars := map[string]decimal.Decimal{}
	for k, s := range v.Vars {
		vars[k] = decimal.RequireFromString(s)
	}
	for _, c := range v.Cases {
		e, err := ParseExpr(c.Expr)
		if err != nil {
			if c.Want != "error" {
				t.Errorf("%q: %v", c.Expr, err)
			}
			continue
		}
		got, err := e.Eval(vars)
		switch {
		case c.Want == "error" && err == nil:
			t.Errorf("%q = %v, want an error", c.Expr, got)
		case c.Want != "error" && (err != nil || got.String() != c.Want):
			t.Errorf("%q = %v, %v; want %s", c.Expr, got, err, c.Want)
		}
	}
	for _, src := range v.Refuse {
		if _, err := ParseExpr(src); err == nil {
			t.Errorf("%q was accepted", src)
		}
	}
}
