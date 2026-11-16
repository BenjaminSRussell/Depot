-- #32: the CPI, SGCN, ITIP and ADI EPC schemes (GS1 TDS 2.3). ADI-var is 68 to 434 bits, so a read is 17 to 112
-- hexadecimal characters (434 bits padded to a 16-bit word); CPI-var is 86 to 224 bits and ITIP-212 212 bits.
ALTER TABLE rfid_read DROP CONSTRAINT IF EXISTS rfid_read_epc_hex_check;
ALTER TABLE rfid_read ADD CONSTRAINT rfid_read_epc_hex_check CHECK (epc_hex ~ '^[0-9A-F]{17,112}$');
