package domain

import (
	"strings"
	"testing"

	"github.com/shopspring/decimal"
)

func TestExpr_EvaluatesExactDecimals(t *testing.T) {
	vars := map[string]decimal.Decimal{"gross": dec("12.50"), "tare": dec("0.30"), "n": dec("4"), "zero": dec("0")}
	for src, want := range map[string]string{
		"gross - tare":                  "12.2",
		"0.1 + 0.2":                     "0.3",
		"(gross - tare) / n":            "3.05",
		"-gross + 20":                   "7.5",
		"2 + 3 * 4":                     "14",
		"(2 + 3) * 4":                   "20",
		"10 / 4":                        "2.5",
		"1 / 3":                         "0.333333333333",
		"abs(tare - gross)":             "12.2",
		"min(gross, tare, n)":           "0.3",
		"max(gross, n)":                 "12.5",
		"round(gross / 3, 2)":           "4.17",
		"floor(gross)":                  "12",
		"ceil(gross)":                   "13",
		"gross > 10 && n == 4":          "true",
		"gross > 10 and n != 4":         "false",
		"not (gross < 10) or zero > 1":  "true",
		"zero != 0 && 10 / zero > 2":    "false", // short circuit: no division by zero
		"gross <= 12.5 || 1 / zero > 1": "true",
		"gross >= 12.51":                "false",
		"!(gross == 12.5)":              "false",
		"  gross   *n ":                 "50",
	} {
		e, err := ParseExpr(src)
		if err != nil {
			t.Errorf("%q: %v", src, err)
			continue
		}
		got, err := e.Eval(vars)
		if err != nil || got.String() != want {
			t.Errorf("%q = %v, %v; want %s", src, got, err, want)
		}
	}
}

func TestExpr_TypesAndNames(t *testing.T) {
	e, err := ParseExpr("b * 2 + a - b")
	if err != nil || e.Type() != ExprNumber || strings.Join(e.Names(), ",") != "a,b" {
		t.Fatalf("number: %+v, %v", e, err)
	}
	if c, err := ParseExpr("a > 1 and b < 2"); err != nil || c.Type() != ExprBool {
		t.Fatalf("condition: %+v, %v", c, err)
	}
	if _, err := e.Eval(map[string]decimal.Decimal{"a": dec("1")}); err == nil || !strings.Contains(err.Error(), "b is not known") {
		t.Fatalf("missing value = %v", err)
	}
	if _, err := ParseExpr("1 / x"); err != nil {
		t.Fatal(err)
	}
	e, _ = ParseExpr("1 / x")
	if _, err := e.Eval(map[string]decimal.Decimal{"x": dec("0")}); err == nil || !strings.Contains(err.Error(), "division by zero") {
		t.Fatalf("division by zero = %v", err)
	}
}

// The parser is the only door: nothing that is not in the grammar runs, and the mistakes people make are named.
func TestExpr_RefusesWhatIsNotTheGrammar(t *testing.T) {
	deep := strings.Repeat("(", 40) + "1" + strings.Repeat(")", 40)
	long := "1" + strings.Repeat(" + 1", 120)
	for name, src := range map[string]string{
		"empty":               "  ",
		"unknown function":    "sqrt(4)",
		"call of a name":      "system(1)",
		"statement":           "1; 2",
		"assignment":          "a = 1",
		"string":              `"x"`,
		"member access":       "a.b",
		"bracket index":       "a[0]",
		"upper-case name":     "Gross + 1",
		"unbalanced":          "(1 + 2",
		"stray close":         "1 + 2)",
		"dangling operator":   "1 +",
		"two operators":       "1 + * 2",
		"number then number":  "1 2",
		"bad number":          "1.2.3",
		"number with a name":  "2x",
		"truth in arithmetic": "(a > 1) + 2",
		"number in a truth":   "a and b",
		"number not":          "not a",
		"compare truths":      "(a > 1) == (b > 1)",
		"chained compare":     "1 < a < 3",
		"arity":               "round(1)",
		"arity abs":           "abs(1, 2)",
		"no args":             "min()",
		"truth argument":      "abs(a > 1)",
		"too deep":            deep,
		"too long":            long,
		"too many characters": strings.Repeat("1", MaxExprLen+1),
		"template literal":    "${a}",
		"backtick":            "`a`",
		"semicolon":           "a;",
		"pipe":                "a | b",
		"ampersand":           "a & b",
	} {
		if _, err := ParseExpr(src); err == nil {
			t.Errorf("%s: %q was accepted", name, src)
		}
	}
}

func FuzzExprParse(f *testing.F) {
	for _, s := range []string{"a + b", "min(a, 2) > 1 and not (b == 0)", "((((", "1 / 0", "a.b", "round(1,2)"} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		e, err := ParseExpr(src)
		if err != nil {
			return
		}
		vars := map[string]decimal.Decimal{}
		for _, n := range e.Names() {
			vars[n] = dec("3")
		}
		_, _ = e.Eval(vars) // must not panic
	})
}
