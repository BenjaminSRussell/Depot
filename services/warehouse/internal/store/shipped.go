package store

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5"

	evts "github.com/BenjaminSRussell/passage/services/warehouse/internal/events"
)

// ShipLine is what a container holds of one line of the fulfillment shipment; Qty is a decimal string above zero.
type ShipLine struct {
	ShipmentLineID string `json:"shipmentLineId"`
	Qty            string `json:"qty"`
}

// ShipContainer is one container that left, with the pack data the warehouse recorded: the pallet it sits on (ParentSSCC), its gross
// weight in kilograms and what it holds. All of them are optional; a confirmation without lines names only the SSCCs.
type ShipContainer struct {
	SSCC       string     `json:"sscc"`
	ParentSSCC string     `json:"parentSscc,omitempty"`
	GrossKg    string     `json:"grossKg,omitempty"`
	Lines      []ShipLine `json:"lines,omitempty"`
}

// ShipConfirmation is the warehouse's confirmation that a fulfillment shipment left the dock without a packing slip posted for
// it (#35, 35-1). SSCCs, when given, are the containers that left; otherwise every closed container of the shipment did.
type ShipConfirmation struct {
	ShipmentID    string    `json:"shipmentId"`
	LegalEntityID string    `json:"legalEntityId"`
	WarehouseID   string    `json:"warehouseId"`
	CarrierCode   string    `json:"carrierCode,omitempty"`
	ShippedAt     time.Time `json:"shippedAt"`
	SSCCs         []string  `json:"ssccs"`
	// Containers is the pack hierarchy, when the caller recorded one; SSCCs then lists the same containers' SSCCs.
	Containers  []ShipContainer `json:"containers"`
	ConfirmedBy string          `json:"confirmedBy"`
	CreatedAt   time.Time       `json:"createdAt"`
}

// ErrShipmentShipped is a second confirmation of a shipment the warehouse already confirmed shipped. A shipment leaves in one
// confirmation: a split load is shipped by packing slips in fulfillment, whose asn.issued notifies each slip.
var ErrShipmentShipped = errors.New("store: the shipment was already confirmed shipped")

// ConfirmShipped records the confirmation and enqueues shipment.shipped in the same transaction. A shipment is confirmed
// once: a second confirmation under another Idempotency-Key is ErrShipmentShipped, a repeat of the key replays the first answer.
func (s *Store) ConfirmShipped(ctx context.Context, idem Idem, c ShipConfirmation) (ShipConfirmation, bool, error) {
	var (
		out      ShipConfirmation
		replayed bool
	)
	if c.SSCCs == nil {
		c.SSCCs = []string{}
	}
	if c.Containers == nil {
		c.Containers = []ShipContainer{}
	}
	packed, err := json.Marshal(c.Containers)
	if err != nil {
		return ShipConfirmation{}, false, err
	}
	err = s.inTx(ctx, func(tx pgx.Tx) error {
		var err error
		out, replayed, err = once(ctx, tx, idem, http.StatusCreated, func() (ShipConfirmation, error) {
			if err := tx.QueryRow(ctx, `insert into shipment_shipped (tenant_id, legal_entity_id, shipment_id, warehouse_id, carrier_code,
				shipped_at, ssccs, containers, confirmed_by) values ($1, $2, $3, $4, $5, $6, $7, $8, $9) on conflict (tenant_id, shipment_id) do nothing returning created_at`,
				tenantID(ctx), c.LegalEntityID, c.ShipmentID, c.WarehouseID, c.CarrierCode, c.ShippedAt, c.SSCCs, packed, c.ConfirmedBy).Scan(&c.CreatedAt); errors.Is(err, pgx.ErrNoRows) {
				return c, ErrShipmentShipped
			} else if err != nil {
				return c, err
			}
			data := evts.ShipmentShippedData{ShipmentID: c.ShipmentID, LegalEntityID: c.LegalEntityID, WarehouseID: c.WarehouseID,
				CarrierCode: c.CarrierCode, ShippedAt: c.ShippedAt.UTC().Format(time.RFC3339), Containers: []evts.ShippedContainer{}, User: c.ConfirmedBy}
			packedBy := map[string]ShipContainer{}
			for _, pc := range c.Containers {
				packedBy[pc.SSCC] = pc
			}
			for _, s := range c.SSCCs {
				ec := evts.ShippedContainer{SSCC: s}
				if pc, ok := packedBy[s]; ok {
					ec.ParentSSCC, ec.GrossKg = pc.ParentSSCC, pc.GrossKg
					for _, l := range pc.Lines {
						ec.Lines = append(ec.Lines, evts.ShippedLine{ShipmentLineID: l.ShipmentLineID, Qty: l.Qty})
					}
				}
				data.Containers = append(data.Containers, ec)
			}
			return c, enqueueFor(ctx, tx, "shipment", c.ShipmentID, evts.ShipmentShipped, data)
		})
		return err
	})
	return out, replayed, err
}

// GetShipConfirmation reads the confirmation of a shipment.
func (s *Store) GetShipConfirmation(ctx context.Context, shipmentID string) (ShipConfirmation, error) {
	var c ShipConfirmation
	err := s.inTx(ctx, func(tx pgx.Tx) error {
		var packed []byte
		if err := tx.QueryRow(ctx, `select shipment_id, legal_entity_id, warehouse_id, carrier_code, shipped_at, ssccs, containers, confirmed_by, created_at
			from shipment_shipped where tenant_id = $1 and shipment_id = $2`, tenantID(ctx), shipmentID).
			Scan(&c.ShipmentID, &c.LegalEntityID, &c.WarehouseID, &c.CarrierCode, &c.ShippedAt, &c.SSCCs, &packed, &c.ConfirmedBy, &c.CreatedAt); err != nil {
			return err
		}
		return json.Unmarshal(packed, &c.Containers)
	})
	return c, err
}
