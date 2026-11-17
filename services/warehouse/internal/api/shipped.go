package api

import (
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/BenjaminSRussell/depot/platform/authz"
	"github.com/BenjaminSRussell/depot/platform/httpx"
	"github.com/BenjaminSRussell/depot/platform/ids"
	"github.com/BenjaminSRussell/depot/platform/tenant"
	"github.com/BenjaminSRussell/depot/services/warehouse/internal/domain"
	"github.com/BenjaminSRussell/depot/services/warehouse/internal/store"
)

const (
	codeShipmentNotFound = "WMS-SHIPMENT-NOT-FOUND"
	// codeShipmentShipped is a second confirmation of a shipment: a split load ships by packing slips in fulfillment.
	codeShipmentShipped = "WMS-SHIPMENT-SHIPPED"
	maxShippedSSCCs     = 500
	maxShipLines        = 200
	// shipClockSkew is how far in the future a ship time may be: a handheld's clock may run a little ahead.
	shipClockSkew = 15 * time.Minute
)

var (
	carrierCode  = regexp.MustCompile(`^[A-Z0-9]{2,4}$`)
	grossKgValue = regexp.MustCompile(`^\d{1,9}(\.\d{1,4})?$`)
	shipQtyValue = regexp.MustCompile(`^\d{1,12}(\.\d{1,6})?$`)
)

// packedContainers checks the pack hierarchy and returns the containers for the store and their SSCCs in order. A carton sits on a
// pallet of the same confirmation (one level, so a pallet cannot sit on a pallet), a container that is not a pallet must hold at
// least one line, and every quantity is a decimal above zero. Errors are keyed by the field, like the other fields of the request.
func packedContainers(in []shipContainerInput, fields map[string]string) ([]store.ShipContainer, []string) {
	if len(in) > maxShippedSSCCs {
		fields["containers"] = fmt.Sprintf("must hold at most %d containers", maxShippedSSCCs)
		return nil, nil
	}
	index := make(map[string]int, len(in))
	for i, c := range in {
		key := fmt.Sprintf("containers[%d]", i)
		switch {
		case !domain.ValidSSCC(c.SSCC):
			fields[key+".sscc"] = "must be an 18-digit SSCC with a valid check digit"
		case func() bool { _, dup := index[c.SSCC]; return dup }():
			fields[key+".sscc"] = "is listed twice"
		}
		index[c.SSCC] = i
	}
	isParent := map[string]bool{}
	for _, c := range in {
		if c.ParentSSCC != "" {
			isParent[c.ParentSSCC] = true
		}
	}
	out := make([]store.ShipContainer, 0, len(in))
	ssccs := make([]string, 0, len(in))
	for i, c := range in {
		key := fmt.Sprintf("containers[%d]", i)
		if c.ParentSSCC != "" {
			pi, ok := index[c.ParentSSCC]
			switch {
			case c.ParentSSCC == c.SSCC:
				fields[key+".parentSscc"] = "cannot be the container itself"
			case !ok:
				fields[key+".parentSscc"] = "must be another container of this confirmation"
			case in[pi].ParentSSCC != "":
				fields[key+".parentSscc"] = "must be a pallet that sits on nothing (cartons go on a pallet, a pallet goes on the truck)"
			}
		}
		if c.GrossKg != "" && !grossKgValue.MatchString(c.GrossKg) {
			fields[key+".grossKg"] = "must be a decimal number of kilograms"
		}
		if len(c.Lines) > maxShipLines {
			fields[key+".lines"] = fmt.Sprintf("must hold at most %d lines", maxShipLines)
			continue
		}
		if len(c.Lines) == 0 && !isParent[c.SSCC] {
			fields[key+".lines"] = "must say what the container holds (a pallet that only carries cartons needs none)"
		}
		lines := make([]store.ShipLine, 0, len(c.Lines))
		for j, l := range c.Lines {
			lk := fmt.Sprintf("%s.lines[%d]", key, j)
			if !ids.Valid(strings.ToLower(l.ShipmentLineID)) {
				fields[lk+".shipmentLineId"] = "must be a line of the shipment (a UUID)"
			}
			if !shipQtyValue.MatchString(l.Qty) || strings.Trim(l.Qty, "0.") == "" {
				fields[lk+".qty"] = "must be a decimal above zero"
			}
			lines = append(lines, store.ShipLine{ShipmentLineID: strings.ToLower(l.ShipmentLineID), Qty: l.Qty})
		}
		out = append(out, store.ShipContainer{SSCC: c.SSCC, ParentSSCC: c.ParentSSCC, GrossKg: c.GrossKg, Lines: lines})
		ssccs = append(ssccs, c.SSCC)
	}
	return out, ssccs
}

func (a *API) registerShipped(r chi.Router) {
	r.With(authz.RequireDuty(dutyShipConfirm)).Post("/v1/warehouse/shipments/{id}:ship", a.confirmShipped)
	r.With(authz.RequireDuty(dutyInquiry)).Get("/v1/warehouse/shipments/{id}/shipped", a.getShipped)
}

type shipInput struct {
	WarehouseID   string     `json:"warehouseId"`
	LegalEntityID string     `json:"legalEntityId"`
	CarrierCode   string     `json:"carrierCode"`
	ShippedAt     *time.Time `json:"shippedAt"`
	SSCCs         []string   `json:"ssccs"`
	// Containers is the pack hierarchy of the load: the containers that left with the pallet each sits on, its weight and what
	// it holds. Give it or ssccs, not both; the confirmation then names the same containers.
	Containers []shipContainerInput `json:"containers"`
}

type shipContainerInput struct {
	SSCC       string          `json:"sscc"`
	ParentSSCC string          `json:"parentSscc"`
	GrossKg    string          `json:"grossKg"`
	Lines      []shipLineInput `json:"lines"`
}

type shipLineInput struct {
	ShipmentLineID string `json:"shipmentLineId"`
	Qty            string `json:"qty"`
}

// confirmShipped records that a fulfillment shipment left the dock without a packing slip posted for it and publishes
// shipment.shipped (#35, 35-1), so the EDI engine still sends the shipment's 856 or DESADV. The legal entity defaults to the
// caller's and must be in its scope.
func (a *API) confirmShipped(w http.ResponseWriter, r *http.Request) {
	shipmentID := strings.ToLower(chi.URLParam(r, "id"))
	var in shipInput
	fields := map[string]string{}
	idem, err := command(r, &in, fields)
	if err != nil {
		httpx.Fail(w, r, err)
		return
	}
	p, _ := tenant.FromContext(r.Context())
	if !ids.Valid(shipmentID) {
		fields["id"] = "must be a UUID"
	}
	if !ids.Valid(in.WarehouseID) {
		fields["warehouseId"] = "must be a UUID"
	}
	le := strings.ToLower(strings.TrimSpace(in.LegalEntityID))
	if le == "" {
		le = p.LegalEntityID
	}
	if !ids.Valid(le) || !tenant.InLegalEntityScope(p, le) {
		fields["legalEntityId"] = "must be a legal entity of the caller"
	}
	if in.CarrierCode != "" && !carrierCode.MatchString(in.CarrierCode) {
		fields["carrierCode"] = "must be 2 to 4 upper-case letters or digits (the SCAC)"
	}
	now := time.Now().UTC()
	shippedAt := now
	if in.ShippedAt != nil {
		shippedAt = in.ShippedAt.UTC()
		if shippedAt.After(now.Add(shipClockSkew)) {
			fields["shippedAt"] = "must not be in the future"
		}
	}
	if len(in.SSCCs) > maxShippedSSCCs {
		fields["ssccs"] = fmt.Sprintf("must hold at most %d containers", maxShippedSSCCs)
	}
	seen := map[string]bool{}
	for i, s := range in.SSCCs {
		switch {
		case !domain.ValidSSCC(s):
			fields[fmt.Sprintf("ssccs[%d]", i)] = "must be an 18-digit SSCC with a valid check digit"
		case seen[s]:
			fields[fmt.Sprintf("ssccs[%d]", i)] = "is listed twice"
		}
		seen[s] = true
	}
	packed := []store.ShipContainer(nil)
	if len(in.Containers) > 0 {
		if len(in.SSCCs) > 0 {
			fields["containers"] = "give either ssccs or containers, not both: containers already name each SSCC"
		} else {
			var ssccs []string
			if packed, ssccs = packedContainers(in.Containers, fields); len(fields) == 0 {
				in.SSCCs = ssccs
			}
		}
	}
	if len(fields) > 0 {
		validationFailed(w, r, fields)
		return
	}
	out, replayed, err := a.st.ConfirmShipped(r.Context(), idem, store.ShipConfirmation{ShipmentID: shipmentID, LegalEntityID: le,
		WarehouseID: strings.ToLower(in.WarehouseID), CarrierCode: in.CarrierCode, ShippedAt: shippedAt.Truncate(time.Second),
		SSCCs: in.SSCCs, Containers: packed, ConfirmedBy: p.UserID})
	if errors.Is(err, store.ErrShipmentShipped) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusConflict, codeShipmentShipped,
			"the shipment was already confirmed shipped; a shipment that leaves in several loads is shipped by packing slips in fulfillment"))
		return
	}
	if err != nil {
		fail(w, r, err, codeShipmentNotFound)
		return
	}
	answer(w, http.StatusCreated, replayed, out)
}

func (a *API) getShipped(w http.ResponseWriter, r *http.Request) {
	id := strings.ToLower(chi.URLParam(r, "id"))
	if !ids.Valid(id) {
		httpx.Fail(w, r, httpx.NewProblem(http.StatusNotFound, codeShipmentNotFound, "not found"))
		return
	}
	out, err := a.st.GetShipConfirmation(r.Context(), id)
	if err != nil {
		fail(w, r, err, codeShipmentNotFound)
		return
	}
	httpx.JSON(w, http.StatusOK, out)
}
