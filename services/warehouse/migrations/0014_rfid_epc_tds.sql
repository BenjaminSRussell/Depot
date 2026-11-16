-- #32: the CPI, SGCN, ITIP and ADI EPC schemes (GS1 TDS 2.3). ADI-var is 68 to 434 bits, so a read is 17 to 112
-- hexadecimal characters (434 bits padded to a 16-bit word); CPI-var is 86 to 224 bits and ITIP-212 212 bits.
ALTER TABLE rfid_read DROP CONSTRAINT IF EXISTS rfid_read_epc_hex_check;
ALTER TABLE rfid_read ADD CONSTRAINT rfid_read_epc_hex_check CHECK (epc_hex ~ '^[0-9A-F]{17,112}$');

-- Reads are stored in the canonical form of domain.EPC.Hex (the tag's bits padded to a whole 16-bit word), so one
-- tag reported with more or less padding is deduplicated as one. The only schemes stored before this migration whose
-- reads could be spelled shorter are the fixed-length ones longer than 96 bits: pad those to their word. The table
-- forces row-level security, so the owner lifts it for this data fix (every tenant's rows) and sets it again.
ALTER TABLE rfid_read NO FORCE ROW LEVEL SECURITY;
UPDATE rfid_read SET epc_hex = rpad(epc_hex, 52, '0') WHERE left(epc_hex, 2) IN ('36', '38', '39') AND char_length(epc_hex) < 52;
UPDATE rfid_read SET epc_hex = rpad(epc_hex, 44, '0') WHERE left(epc_hex, 2) IN ('37', '3E') AND char_length(epc_hex) < 44;
ALTER TABLE rfid_read FORCE ROW LEVEL SECURITY;
