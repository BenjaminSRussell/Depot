-- Validates the check of 0023 (SHARE UPDATE EXCLUSIVE, so open items keep being written).
ALTER TABLE ap_open_item VALIDATE CONSTRAINT ap_open_item_trading_partner_check;
