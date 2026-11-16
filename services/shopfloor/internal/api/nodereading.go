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

func (a *API) registerNodeReadings(r chi.Router) {
	r.With(authz.RequireDuty(DutyEdgeConnect)).Post("/v1/shopfloor/edge-nodes/{id}/readings", a.reportNodeReadings)
	r.With(authz.RequireDuty(DutyControlRecipeExecute)).Get("/v1/shopfloor/node-readings", a.getNodeReading)
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

func (a *API) getNodeReading(w http.ResponseWriter, r *http.Request) {
	if !validTenant(w, r) {
		return
	}
	node := strings.TrimSpace(r.URL.Query().Get("node"))
	if node == "" || len(node) > 300 {
		validationFailed(w, r, map[string]string{"node": "is required: the node or tag to read, at most 300 characters"})
		return
	}
	out, err := a.store.LatestNodeReading(r.Context(), node, NodeReadingMaxAge, a.now())
	if err != nil {
		a.fail(w, r, "read node", err)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
