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

	ClassCUI     = "CUI"
	ClassNONE    = "NONE"
	ClassUNKNOWN = "UNKNOWN"

	SourceDefault     = "DEFAULT"
	SourceTenant      = "TENANT"
	SourceLegalEntity = "LEGAL_ENTITY"
	SourceRecord      = "RECORD"
	SourceUnresolved  = "UNRESOLVED"

	EntityProject = "project"

	CodeCUIBoundary      = "COM-1301"
	CodeTooManyRefs      = "COM-1303"
	CodeOwnerUnavailable = "COM-1304"
	CodeRecordNotFound   = "COM-1305"

	DutyDataDesignationWrite = "datadesignation.write"
	DutyDataDesignationRead  = "datadesignation.read"

	DutyDataDesignationMarkingRead = "datadesignation.marking.read"

	DutyDataDesignationResolve = "compliance.data-designation.resolve"

	MaxResolveRefs = 500

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

var SupportedEntityTypes = map[string]bool{EntityProject: true}

func ParseEntityType(v string) (string, bool) {
	v = strings.ToLower(strings.TrimSpace(v))
	return v, SupportedEntityTypes[v]
}

func Effective(tenantCUI, legalEntityCUI, recordCUI bool) (class, source string) {
	switch {
	case recordCUI:
		return ClassCUI, SourceRecord
	case legalEntityCUI:
		return ClassCUI, SourceLegalEntity
	case tenantCUI:
		return ClassCUI, SourceTenant
	}
	return ClassNONE, SourceDefault
}
