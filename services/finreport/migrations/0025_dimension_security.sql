ALTER TABLE statement_run ADD COLUMN IF NOT EXISTS dimension_scope text NOT NULL DEFAULT '';
ALTER TABLE statement_cell ADD COLUMN IF NOT EXISTS filtered_amount numeric NOT NULL DEFAULT 0;
