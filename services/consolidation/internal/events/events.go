package events

import "github.com/BenjaminSRussell/depot/platform/events"

const (
	Context = "consol"
	Source  = "/depot/consolidation"
)

var RunCompleted = events.Type(Context, "run", "completed", 1)

// RunCompletedData is the payload of RunCompleted (PRD 7.40.5).
type RunCompletedData struct {
	RunID            string `json:"runId"`
	GroupID          string `json:"groupId"`
	Period           string `json:"period"`
	Currency         string `json:"currency"`
	Version          int    `json:"version"`
	UnitCount        int    `json:"unitCount"`
	EliminationCount int    `json:"eliminationCount"`
	CTAAmount        string `json:"ctaAmount"`
}
