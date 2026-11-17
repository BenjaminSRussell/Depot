package domain

import (
	"time"

	"github.com/shopspring/decimal"
)

// WarehouseShip is the warehouse's confirmation that a fulfillment shipment left the dock without a packing slip posted for it
// (dev.depot.wms.shipment.shipped.v1, #35 35-1). SSCCs, when given, are the containers that left; otherwise every closed
// container of the shipment did.
type WarehouseShip struct {
	ShippedAt   time.Time
	CarrierCode string
	SSCCs       []string
	// Containers is the pack hierarchy the warehouse recorded: each container with the pallet it sits on, its weight and what it
	// holds. When any container holds lines, the 856 or DESADV is built from these and not from fulfillment's containers; a
	// confirmation that named only SSCCs falls back to fulfillment's.
	Containers []WarehouseContainer
}

// WarehouseContainer is one container of the warehouse's pack hierarchy, as shipment.shipped carries it.
type WarehouseContainer struct {
	SSCC       string
	ParentSSCC string
	GrossKg    decimal.Decimal
	Lines      []SourceContainerLine
}

// Packed reports whether the warehouse recorded what the containers hold, so the pack hierarchy comes from it.
func (ws WarehouseShip) Packed() bool {
	for _, c := range ws.Containers {
		if len(c.Lines) > 0 {
			return true
		}
	}
	return false
}

// ShippedByWarehouse returns the shipment as the warehouse shipped it, so the whole-shipment 856 or DESADV can be built from it:
// status SHIPPED, the warehouse's ship time as the actual ship date, its carrier when it named one, and only the containers that
// left, marked SHIPPED (a pallet named takes the cartons on it along). When the warehouse recorded how the load was packed
// (ws.Containers with lines), those containers are the pack hierarchy, built from the warehouse's data and not from fulfillment's;
// otherwise fulfillment's containers are used. The shipment must be the partner's. A shipment fulfillment already shipped by a packing slip is
// refused: its notices come from that slip's asn.issued, and sending the whole shipment too would notify its containers twice. A
// container already on a slip, or one the shipment does not hold, is refused for the same reason.
func ShippedByWarehouse(p TradeProfile, sh SourceShipment, ws WarehouseShip) (SourceShipment, error) {
	if err := checkScope(p, sh.LegalEntityID, sh.CustomerID); err != nil {
		return sh, err
	}
	switch sh.Status {
	case "PICKED", "PACKED":
	case "SHIPPED", "PARTIALLY_SHIPPED", "DELIVERED":
		return sh, outboundf("shipment %s was shipped by a packing slip; its notice comes from that slip", sh.ShipmentNumber)
	default:
		return sh, outboundf("shipment %s is %s; only a picked or packed shipment can leave without a packing slip", sh.ShipmentNumber, sh.Status)
	}
	if ws.ShippedAt.IsZero() {
		return sh, outboundf("the warehouse confirmation of shipment %s has no ship time", sh.ShipmentNumber)
	}
	if ws.Packed() {
		// The warehouse recorded how the load was packed: its containers are the pack hierarchy, whatever fulfillment holds.
		packed, err := warehousePack(sh, ws)
		if err != nil {
			return sh, err
		}
		at := ws.ShippedAt.UTC()
		sh.Status, sh.ActualShipDate, sh.Containers = "SHIPPED", &at, packed
		if ws.CarrierCode != "" {
			code := ws.CarrierCode
			sh.CarrierCode = &code
		}
		return sh, nil
	}
	named := make(map[string]bool, len(ws.SSCCs))
	for _, s := range ws.SSCCs {
		named[s] = true
	}
	byID := make(map[string]SourceContainer, len(sh.Containers))
	for _, c := range sh.Containers {
		if c.ID != "" {
			byID[c.ID] = c
		}
	}
	// left reports whether c left: it is named, or every container left (none named), or the pallet it sits on left.
	var left func(c SourceContainer, depth int) bool
	left = func(c SourceContainer, depth int) bool {
		if len(named) == 0 || named[c.SSCC] {
			return true
		}
		if depth > 0 || c.ParentContainerID == nil {
			return false
		}
		parent, ok := byID[*c.ParentContainerID]
		return ok && left(parent, depth+1)
	}
	kept := make([]SourceContainer, 0, len(sh.Containers))
	found := map[string]bool{}
	for _, c := range sh.Containers {
		if !left(c, 0) {
			continue
		}
		found[c.SSCC] = true
		if c.PackingSlipID != nil && *c.PackingSlipID != "" {
			return sh, outboundf("container %s of shipment %s already shipped with a packing slip", c.SSCC, sh.ShipmentNumber)
		}
		if c.Status != "CLOSED" {
			if len(named) > 0 {
				return sh, outboundf("container %s of shipment %s is %s, not closed", c.SSCC, sh.ShipmentNumber, c.Status)
			}
			continue
		}
		c.Status = "SHIPPED"
		kept = append(kept, c)
	}
	for s := range named {
		if !found[s] {
			return sh, outboundf("the warehouse shipped container %s, which shipment %s does not hold", s, sh.ShipmentNumber)
		}
	}
	at := ws.ShippedAt.UTC()
	sh.Status, sh.ActualShipDate, sh.Containers = "SHIPPED", &at, kept
	if ws.CarrierCode != "" {
		code := ws.CarrierCode
		sh.CarrierCode = &code
	}
	return sh, nil
}

// NoticeSSCCs are the containers an advance ship notice of the shipment (or of the slip) lists: the shipped ones.
func NoticeSSCCs(sh SourceShipment, slip *SlipRef) []string {
	var out []string
	for _, c := range OnlySlip(sh, slip).Containers {
		if c.Status == "SHIPPED" && c.SSCC != "" {
			out = append(out, c.SSCC)
		}
	}
	return out
}

// KeepSSCCs narrows the notice to the containers in keep: a slip notice lists only those, a whole-shipment notice drops the others.
func KeepSSCCs(sh SourceShipment, slip *SlipRef, keep []string) (SourceShipment, *SlipRef) {
	in := make(map[string]bool, len(keep))
	for _, s := range keep {
		in[s] = true
	}
	if slip.perSlip() {
		narrowed := &SlipRef{ID: slip.ID, Number: slip.Number}
		for _, s := range slip.SSCCs {
			if in[s] {
				narrowed.SSCCs = append(narrowed.SSCCs, s)
			}
		}
		return sh, narrowed
	}
	kept := make([]SourceContainer, 0, len(sh.Containers))
	for _, c := range sh.Containers {
		if in[c.SSCC] {
			kept = append(kept, c)
		}
	}
	sh.Containers = kept
	return sh, slip
}

// warehousePack builds the containers of the notice from the warehouse's pack hierarchy. Each container's id is its SSCC, a carton
// points at its pallet's, every line must be a line of the shipment, and a container that fulfillment already put on a packing slip
// is refused (its slip's notice lists it).
func warehousePack(sh SourceShipment, ws WarehouseShip) ([]SourceContainer, error) {
	lines := make(map[string]bool, len(sh.Lines))
	for _, l := range sh.Lines {
		lines[l.ID] = true
	}
	onSlip := map[string]bool{}
	for _, c := range sh.Containers {
		if c.PackingSlipID != nil && *c.PackingSlipID != "" {
			onSlip[c.SSCC] = true
		}
	}
	seen := make(map[string]bool, len(ws.Containers))
	for _, c := range ws.Containers {
		seen[c.SSCC] = true
	}
	out := make([]SourceContainer, 0, len(ws.Containers))
	dup := make(map[string]bool, len(ws.Containers))
	for _, c := range ws.Containers {
		if c.SSCC == "" {
			return nil, outboundf("the warehouse listed a container of shipment %s without an SSCC", sh.ShipmentNumber)
		}
		if dup[c.SSCC] {
			return nil, outboundf("the warehouse listed container %s of shipment %s twice", c.SSCC, sh.ShipmentNumber)
		}
		dup[c.SSCC] = true
		if onSlip[c.SSCC] {
			return nil, outboundf("container %s of shipment %s already shipped with a packing slip", c.SSCC, sh.ShipmentNumber)
		}
		if c.ParentSSCC != "" && (!seen[c.ParentSSCC] || c.ParentSSCC == c.SSCC) {
			return nil, outboundf("the warehouse put container %s of shipment %s on %s, which it did not list", c.SSCC, sh.ShipmentNumber, c.ParentSSCC)
		}
		sc := SourceContainer{ID: c.SSCC, SSCC: c.SSCC, Status: "SHIPPED", GrossKg: c.GrossKg, Lines: make([]SourceContainerLine, 0, len(c.Lines))}
		if c.ParentSSCC != "" {
			parent := c.ParentSSCC
			sc.ParentContainerID = &parent
		}
		for _, l := range c.Lines {
			if !lines[l.ShipmentLineID] {
				return nil, outboundf("the warehouse packed a line that shipment %s does not hold into container %s", sh.ShipmentNumber, c.SSCC)
			}
			if !l.Qty.IsPositive() {
				return nil, outboundf("the warehouse packed a quantity of %s into container %s of shipment %s", l.Qty, c.SSCC, sh.ShipmentNumber)
			}
			sc.Lines = append(sc.Lines, l)
		}
		out = append(out, sc)
	}
	return out, nil
}
