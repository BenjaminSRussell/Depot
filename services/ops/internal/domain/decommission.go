package domain

import (
	"regexp"
	"strings"
	"time"
)

// Tenant decommission and masked-clone schedules (DPT-50, part 4). A decommission is the reverse of a provision, runs only for a
// tenant that identity reports as suspended, and is audited. A clone schedule is a recurring masked copy of one tenant's schema.
const (
	CodeDecommissionUnavailable  = "OPS-1730" // identity cannot be asked whether the tenant is suspended
	CodeDecommissionNotSuspended = "OPS-1731"
	CodeDecommissionUnknown      = "OPS-1732"
	CodeDecommissionOrder        = "OPS-1733"
	CodeDecommissionInvalid      = "OPS-1734"
	CodeScheduleInvalid          = "OPS-1735"
	CodeScheduleUnknown          = "OPS-1736"
	CodeScheduleConflict         = "OPS-1737"
	CodePurgeNotReady            = "OPS-1738" // the decommission is not DECOMMISSIONED yet
	CodePurgeNoExport            = "OPS-1739" // no verified export is on record
	CodePurgeUnknown             = "OPS-1740" // no purge is recorded for the tenant
	CodePurgeInvalid             = "OPS-1741" // the export id or purge result is malformed

	DecommissionRunning = "DECOMMISSIONING"
	DecommissionDone    = "DECOMMISSIONED"
	DecommissionFailed  = "FAILED"
)

// DecommissionSteps are the steps of a decommission in the order they run: the reverse of ProvisionSteps without the licence step
// (the licence record is kept as the audit of what the tenant held).
var DecommissionSteps = []string{"lakehouse", "kafka-acls", "databases", "realm"}

// ValidDecommissionStep reports whether a is a decommission step.
func ValidDecommissionStep(a string) bool {
	for _, s := range DecommissionSteps {
		if s == a {
			return true
		}
	}
	return false
}

// ValidateDecommissionReason trims and checks the reason given for decommissioning (it goes into the audit trail).
func ValidateDecommissionReason(reason string) (string, error) {
	reason = strings.TrimSpace(reason)
	if reason == "" || len([]rune(reason)) > 500 {
		return "", fail(CodeDecommissionInvalid, "give the reason for decommissioning (at most 500 characters)")
	}
	return reason, nil
}

var schemaRe = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// CloneSchedule is a recurring masked clone of one tenant's schema (the tenant-restore clone command).
type CloneSchedule struct {
	ID            string     `json:"id"`
	Name          string     `json:"name"`
	SourceTenant  string     `json:"sourceTenant"`
	Schema        string     `json:"schema"`
	MasksFile     string     `json:"masksFile"`
	IntervalHours int        `json:"intervalHours"`
	Enabled       bool       `json:"enabled"`
	NextRunAt     time.Time  `json:"nextRunAt"`
	LastRunAt     *time.Time `json:"lastRunAt,omitempty"`
	LastStatus    string     `json:"lastStatus,omitempty"`
	CreatedBy     string     `json:"createdBy"`
	CreatedAt     time.Time  `json:"createdAt"`
}

// ValidateCloneSchedule normalizes and checks a schedule request.
func ValidateCloneSchedule(s CloneSchedule) (CloneSchedule, error) {
	s.Name = strings.TrimSpace(s.Name)
	s.Schema = strings.TrimSpace(s.Schema)
	s.MasksFile = strings.TrimSpace(s.MasksFile)
	switch {
	case s.Name == "" || len(s.Name) > 100:
		return s, fail(CodeScheduleInvalid, "name is required (at most 100 characters)")
	case !schemaRe.MatchString(s.Schema):
		return s, fail(CodeScheduleInvalid, "schema must be a lower-case Postgres schema name")
	case s.MasksFile == "" || len(s.MasksFile) > 500 || strings.ContainsAny(s.MasksFile, "\x00\n"):
		return s, fail(CodeScheduleInvalid, "masksFile is required: the mask configuration file the clone job reads")
	case s.IntervalHours < 1 || s.IntervalHours > 8760:
		return s, fail(CodeScheduleInvalid, "intervalHours must be between 1 and 8760")
	}
	return s, nil
}
