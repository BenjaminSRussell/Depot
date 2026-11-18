ALTER TABLE invoice ADD COLUMN IF NOT EXISTS ship_to jsonb;
ALTER TABLE invoice_line ADD COLUMN IF NOT EXISTS item_tax_group text CHECK (item_tax_group IS NULL OR length(item_tax_group) <= 64);
