-- Validates the check of 0028 (SHARE UPDATE EXCLUSIVE, so open items keep being written).
ALTER TABLE open_item VALIDATE CONSTRAINT open_item_trading_partner_check;
