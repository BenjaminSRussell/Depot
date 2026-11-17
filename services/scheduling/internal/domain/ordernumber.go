package domain

import "strings"

// MaxOrderNumber is the longest order number an operation accepts, in
// characters: the most a formatted number sequence value can hold (FR-5.8).
const MaxOrderNumber = 40

// CompareOrderNumbers orders two order numbers the way people read them: runs
// of ASCII digits compare by numeric value and the text between them compares
// as text, so PO-999999 comes before PO-1000000 and WO-87 before WO-100.
// Production formats its numbers with a minimum width (PO-%06d), which a
// plain string compare gets wrong once a number outgrows that width, and
// opening-WIP imports carry legacy numbers of any shape. Numbers that are
// equal piece by piece (PO-7 and PO-007) fall back to a plain string compare,
// so the order is total and deterministic.
func CompareOrderNumbers(a, b string) int {
	ra, rb := a, b
	for ra != "" && rb != "" {
		ta, da := nextRun(ra)
		tb, db := nextRun(rb)
		ra, rb = ra[len(ta):], rb[len(tb):]
		var c int
		if da && db {
			c = compareDigits(ta, tb)
		} else {
			c = strings.Compare(ta, tb)
		}
		if c != 0 {
			return c
		}
	}
	switch {
	case ra == "" && rb != "":
		return -1
	case ra != "" && rb == "":
		return 1
	}
	return strings.Compare(a, b)
}

// orderNumberCompare is CompareOrderNumbers with "no number" last.
func orderNumberCompare(a, b string) int {
	switch {
	case a == b:
		return 0
	case a == "":
		return 1
	case b == "":
		return -1
	}
	return CompareOrderNumbers(a, b)
}

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// nextRun returns the leading run of s, all digits or all non-digits, and
// whether it is digits.
func nextRun(s string) (string, bool) {
	digits := isDigit(s[0])
	i := 1
	for i < len(s) && isDigit(s[i]) == digits {
		i++
	}
	return s[:i], digits
}

// compareDigits compares two digit runs by value without parsing them, so a
// run of any length works: leading zeros dropped, then the longer run is the
// larger number, then digit by digit.
func compareDigits(a, b string) int {
	a, b = strings.TrimLeft(a, "0"), strings.TrimLeft(b, "0")
	if len(a) != len(b) {
		if len(a) < len(b) {
			return -1
		}
		return 1
	}
	return strings.Compare(a, b)
}
