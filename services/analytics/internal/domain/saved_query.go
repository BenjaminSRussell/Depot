package domain

import (
	"strings"
	"unicode/utf8"
)

// MaxLookbackDays is the longest relative range a saved query keeps: the five-year explorer limit.
const MaxLookbackDays = 1826

// DefaultLookbackDays is the range of a saved query that names none.
const DefaultLookbackDays = 30

// CodeSavedQueryDupe is ANA-0115: the user already has a saved query with that name.
const CodeSavedQueryDupe = "ANA-0115"

// SavedQuery is a named explorer query of one user. The range is relative: the last LookbackDays days up to today.
type SavedQuery struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Measures     []string `json:"measures"`
	Dimensions   []string `json:"dimensions"`
	LookbackDays int      `json:"lookbackDays"`
	CreatedAt    string   `json:"createdAt"`
}

// ValidateSavedQuery checks a new saved query against the shape limits and the published catalog: the metrics must be
// published and of one grain, and every dimension must be allowed by every metric (the rules BuildPlan applies).
func ValidateSavedQuery(q SavedQuery, catalog []Metric) *Error {
	fields := map[string]string{}
	if n := utf8.RuneCountInString(strings.TrimSpace(q.Name)); n < 1 || n > 120 {
		fields["name"] = "must be 1 to 120 characters"
	}
	if q.LookbackDays < 1 || q.LookbackDays > MaxLookbackDays {
		fields["lookbackDays"] = "must be 1 to 1826"
	}
	switch {
	case len(q.Measures) == 0:
		fields["measures"] = "at least one measure is required"
	case len(q.Measures) > MaxMeasures:
		fields["measures"] = "at most 10 measures"
	}
	if len(q.Dimensions) > MaxDimensions {
		fields["dimensions"] = "at most 8 dimensions"
	}
	if len(fields) == 0 {
		var grain string
		allowed := map[string]bool{}
		for i, id := range q.Measures {
			m, ok := CatalogMetric(catalog, id)
			if !ok {
				fields["measures"] = "unknown or unpublished metric " + id
				break
			}
			if i == 0 {
				grain = m.Grain
			} else if m.Grain != grain {
				fields["measures"] = "measures must share one grain"
				break
			}
			next := map[string]bool{}
			for _, d := range m.AllowedDimensions {
				if i == 0 || allowed[d] {
					next[d] = true
				}
			}
			allowed = next
		}
		if len(fields) == 0 {
			for _, d := range q.Dimensions {
				if !allowed[d] {
					fields["dimensions"] = "dimension " + d + " is not allowed for these measures"
					break
				}
			}
		}
	}
	if len(fields) > 0 {
		return validation(fields)
	}
	return nil
}
