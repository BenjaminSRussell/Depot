-- Validates the check of 0021 (it only takes a SHARE UPDATE EXCLUSIVE lock, so invoices keep being written).
ALTER TABLE invoice VALIDATE CONSTRAINT invoice_trading_partner_check;
