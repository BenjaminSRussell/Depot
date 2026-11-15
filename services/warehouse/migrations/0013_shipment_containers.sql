-- #35 (35-1): the pack hierarchy the warehouse recorded when it confirmed a shipment shipped: each container's SSCC, the pallet it
-- sits on, its weight and what it holds. shipment.shipped carries it so the EDI engine builds the 856 or DESADV from the
-- warehouse's containers and not from fulfillment's. Empty for a confirmation that named only SSCCs.
ALTER TABLE shipment_shipped ADD COLUMN IF NOT EXISTS containers jsonb NOT NULL DEFAULT '[]'::jsonb;
