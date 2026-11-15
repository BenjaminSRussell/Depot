package domain

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// Per-operation SLOs (PSG-50, FR-50.1). An operation is one route of one service (the chi route pattern the shared HTTP series
// carry as the route label, for example /v1/ledger/journal-entries, across every method registered on it: the series have no method label), with its own availability and p95 objectives. The
// figures are read from the same series as the per-service report; the objectives come from configuration (SLO_OPERATIONS).

// OperationSLO is one objective: a service (scrape job) and a route pattern, with its targets. A zero target falls back to the
// service-wide default of the report.
type OperationSLO struct {
	Name         string  `json:"name"`
	Service      string  `json:"service"`
	Route        string  `json:"route"`
	Availability float64 `json:"availability,omitempty"`
	LatencyP95Ms int     `json:"latencyP95Ms,omitempty"`
}

// ParseOperationSLOs reads `[{"name":"post-journal-entry","service":"ledger","route":"/v1/ledger/journal-entries","availability":0.9995,
// "latencyP95Ms":500}]`. Names are unique; a service and a route are required; an availability is a fraction below 1.
func ParseOperationSLOs(raw string) ([]OperationSLO, error) {
	var ops []OperationSLO
	dec := json.NewDecoder(strings.NewReader(raw))
	// The shared series (http_requests_total{route,status}, http_request_duration_seconds{route}) carry no method, so an objective is per
	// route across all its methods; a method in the configuration is refused rather than silently measuring something else.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ops); err != nil {
		return nil, fail(CodeSLOInvalid, "SLO operations must be a JSON array of objects: %v", err)
	}
	seen := map[string]bool{}
	for i, o := range ops {
		switch {
		case strings.TrimSpace(o.Name) == "" || seen[o.Name]:
			return nil, fail(CodeSLOInvalid, "SLO operation %d needs a unique name", i+1)
		case o.Service == "" || !strings.HasPrefix(o.Route, "/"):
			return nil, fail(CodeSLOInvalid, "SLO operation %s needs a service and a route that starts with /", o.Name)
		case o.Availability < 0 || o.Availability >= 1:
			return nil, fail(CodeSLOInvalid, "SLO operation %s: availability must be a fraction below 1 (0.999)", o.Name)
		case o.LatencyP95Ms < 0:
			return nil, fail(CodeSLOInvalid, "SLO operation %s: latencyP95Ms must not be negative", o.Name)
		}
		seen[o.Name] = true
	}
	return ops, nil
}

// OperationSample is one operation's measured window.
type OperationSample struct {
	Operation OperationSLO
	Requests  float64
	Failed    float64
	P95       *float64
}

// OperationLine is one operation in the report.
type OperationLine struct {
	Name               string   `json:"name"`
	Service            string   `json:"service"`
	Route              string   `json:"route"`
	Requests           float64  `json:"requests"`
	Availability       *float64 `json:"availability"`
	AvailabilityTarget float64  `json:"availabilityTarget"`
	LatencyP95Ms       *float64 `json:"latencyP95Ms"`
	LatencyTargetMs    float64  `json:"latencyTargetMs"`
	ErrorBudgetLeft    *float64 `json:"errorBudgetRemaining"`
	AvailabilityBreach bool     `json:"availabilityBreached"`
	LatencyBreach      bool     `json:"latencyBreached"`
	Breached           bool     `json:"breached"`
	NoTraffic          bool     `json:"noTraffic"`
}

// EvaluateOperations turns samples into lines sorted by name, with the same rules as EvaluateSLO: an operation with no request in
// the window has no availability and is never breached. An operation's own target wins over the service's, which wins over the default.
func EvaluateOperations(samples []OperationSample, t SLOTargets) []OperationLine {
	out := make([]OperationLine, 0, len(samples))
	for _, s := range samples {
		avail, latency := t.Availability, t.LatencyFor(s.Operation.Service)
		if s.Operation.Availability > 0 {
			avail = s.Operation.Availability
		}
		if s.Operation.LatencyP95Ms > 0 {
			latency = time.Duration(s.Operation.LatencyP95Ms) * time.Millisecond
		}
		line := OperationLine{Name: s.Operation.Name, Service: s.Operation.Service, Route: s.Operation.Route, Requests: s.Requests,
			AvailabilityTarget: avail, LatencyTargetMs: float64(latency.Milliseconds())}
		if s.Requests <= 0 {
			line.NoTraffic = true
		} else {
			a := 1 - s.Failed/s.Requests
			line.Availability = &a
			if budget := 1 - avail; budget > 0 {
				left := 1 - (s.Failed/s.Requests)/budget
				line.ErrorBudgetLeft = &left
			}
			line.AvailabilityBreach = a < avail
			if s.P95 != nil {
				ms := *s.P95 * 1000
				line.LatencyP95Ms = &ms
				line.LatencyBreach = ms > line.LatencyTargetMs
			}
		}
		line.Breached = line.AvailabilityBreach || line.LatencyBreach
		out = append(out, line)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// String names an operation for logs.
func (o OperationSLO) String() string { return fmt.Sprintf("%s (%s %s)", o.Name, o.Service, o.Route) }
