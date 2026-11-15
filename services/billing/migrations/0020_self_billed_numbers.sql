-- #35 (35-5): a self-billed invoice keeps the customer's own document number in its own namespace, unique per customer in a legal entity,
-- and takes a billing-side invoice number (SB-<customer>-<number>) that cannot collide with another customer's or billing's sequence.
-- The note says why it was posted on another date than the customer's (the document's period was closed).
ALTER TABLE invoice ADD COLUMN IF NOT EXISTS customer_document_number text;
ALTER TABLE invoice ADD COLUMN IF NOT EXISTS posting_note text;
CREATE UNIQUE INDEX IF NOT EXISTS invoice_customer_document_number ON invoice (tenant_id, legal_entity_id, customer_id, customer_document_number)
  WHERE customer_document_number IS NOT NULL;
