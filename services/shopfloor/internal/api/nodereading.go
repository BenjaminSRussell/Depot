package api

import (
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/shopspring/decimal"

	"github.com/BenjaminSRussell/passage/platform/authz"
	"github.com/BenjaminSRussell/passage/platform/httpx"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/domain"
	"github.com/BenjaminSRussell/passage/services/shopfloor/internal/store"
)

// NodeReadingMaxAge is how old a reading may be and still answer a kiosk's machine readout.
const NodeReadingMaxAge = 5 * time.Minute

const maxReadings = 200

// readingSkew is how far ahead of the service's clock a connector's reading time may be.
const readingSkew = 5 * time.Second

func (a *API) registerNodeReadings(r chi.Router) {
	r.With(authz.RequireDuty(DutyEdgeConnect)).Post("/v1/shopfloor/edge-nodes/{id}/readings", a.reportNodeReadings)
	r.With(authz.RequireDuty(DutyControlRecipeExecute)).Post("/v1/shopfloor/control-recipes/{id}/readouts", a.claimReadout)
}

type readingRequest struct {
	Readings []struct {
		Node     string          `json:"node"`
		Value    decimal.Decimal `json:"value"`
		NodeType string          `json:"nodeType"`
		At       *time.Time      `json:"at"`
	} `json:"readings"`
}

func (a *API) reportNodeReadings(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	edgeID, ok := pathID(w, r, store.CodeEdgeNotFound, "edge node")
	if !ok {
		return
	}
	var req readingRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	f := map[string]string{}
	if len(req.Readings) == 0 || len(req.Readings) > maxReadings {
		f["readings"] = "must hold 1 to 200 readings"
	}
	in := make([]store.NodeReadingIn, 0, len(req.Readings))
	for _, rd := range req.Readings {
		if n := strings.TrimSpace(rd.Node); n == "" || len(n) > 300 {
			f["readings"] = "each reading needs a node of at most 300 characters"
			break
		}
		if !domain.KnownNodeType(rd.NodeType) {
			f["readings"] = "nodeType must be an OPC-UA numeric or Boolean type or left out"
			break
		}
		at := a.now().UTC()
		if rd.At != nil {
			at = rd.At.UTC()
		}
		if at.After(a.now().Add(readingSkew)) {
			f["readings"] = "a reading cannot be from the future: check the connector's clock"
			break
		}
		in = append(in, store.NodeReadingIn{Node: strings.TrimSpace(rd.Node), Value: rd.Value, NodeType: rd.NodeType, At: at})
	}
	if len(f) > 0 {
		validationFailed(w, r, f)
		return
	}
	if err := a.store.ReportNodeReadings(r.Context(), edgeID, connectorSubject(r), in); err != nil {
		a.fail(w, r, "report node readings", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type readoutRequest struct {
	StepKey string `json:"stepKey"`
}

// claimReadout gives a kiosk the connector's latest reading of the node of a machine-readout step of its recipe, and
// records it as the step's claim (see store.ClaimReadout).
func (a *API) claimReadout(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	id, ok := pathID(w, r, store.CodeRecipeNotFound, "control recipe")
	if !ok {
		return
	}
	var req readoutRequest
	if err := httpx.Decode(r, &req); err != nil {
		httpx.Fail(w, r, err)
		return
	}
	if k := strings.TrimSpace(req.StepKey); k == "" || len(k) > 400 {
		validationFailed(w, r, map[string]string{"stepKey": "is required: the key of the machine-readout step"})
		return
	}
	out, err := a.store.ClaimReadout(r.Context(), id, req.StepKey, NodeReadingMaxAge, a.now())
	if err != nil {
		a.fail(w, r, "claim readout", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
