package domain

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

const (
	ScopeTenant      = "TENANT"
	ScopeLegalEntity = "LEGAL_ENTITY"
	ScopeRecord      = "RECORD"

	ClassCUI = "CUI"

	CodeCUIBoundary = "COM-1301"

	DutyDataDesignationWrite = "datadesignation.write"

	MaxDesignationReason   = 500
	MaxDesignationCategory = 100
)

type Boundary string

const (
	BoundaryStandard Boundary = "standard"
	BoundaryCUI      Boundary = "cui"
)

func ParseBoundary(v string) (Boundary, error) {
	switch Boundary(v) {
	case "", BoundaryStandard:
		return BoundaryStandard, nil
	case BoundaryCUI:
		return BoundaryCUI, nil
	}
	return BoundaryStandard, fmt.Errorf("boundary must be standard or cui, got %q", v)
}

func ValidDesignationReason(reason string) bool {
	n := utf8.RuneCountInString(strings.TrimSpace(reason))
	return n >= 1 && n <= MaxDesignationReason
}
