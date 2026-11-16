SET LOCAL lock_timeout = '5s';

ALTER TABLE journal_line VALIDATE CONSTRAINT journal_line_trading_partner_check;
