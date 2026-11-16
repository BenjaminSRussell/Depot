package domain

import (
	"regexp"
	"strings"
)

var outcomeCode = regexp.MustCompile(`^[A-Z0-9][A-Z0-9_-]{1,31}$`)

// Outcome is one attribute outcome a test group can list as passing and a result can record, such as "No visible
// defect". Results and test groups reference it by id; the code is what people and imports type.
type Outcome struct {
	ID      string `json:"id"`
	Code    string `json:"code"`
	Name    string `json:"name"`
	Active  bool   `json:"active"`
	Version int    `json:"version"`
}

// NewOutcome validates a new outcome.
func NewOutcome(code, name string) (Outcome, error) {
	code = strings.ToUpper(strings.TrimSpace(code))
	name = strings.TrimSpace(name)
	if !outcomeCode.MatchString(code) {
		return Outcome{}, invalid("code must be 2 to 32 characters of A-Z, 0-9, _ or -")
	}
	if name == "" || len([]rune(name)) > 120 {
		return Outcome{}, invalid("name must be 1 to 120 characters")
	}
	return Outcome{Code: code, Name: name, Active: true, Version: 1}, nil
}

// ValidateOutcomeName checks a renamed outcome.
func ValidateOutcomeName(name string) error {
	if name = strings.TrimSpace(name); name == "" || len([]rune(name)) > 120 {
		return invalid("name must be 1 to 120 characters")
	}
	return nil
}
