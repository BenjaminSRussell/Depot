package domain

import (
	"errors"
	"fmt"
	"math/big"
	"math/rand/v2"
	"strings"
	"testing"
)

// Tests of the CPI, SGCN, ITIP and ADI schemes against GS1 EPC Tag Data Standard (TDS) Release 2.3, ratified Oct 2025.
// Page numbers are those printed in the TDS 2.3 PDF.

// TDS 2.3 annex E.3, "Summary Examples of All EPC schemes": CPI-96 and CPI-var on page 363, SGCN-96 on page 364,
// ADI-var, ITIP-110 and ITIP-212 on page 365. Each binary encoding decodes to the pure identity URI, tag URI and GS1
// element string the annex gives, and encoding the tag URI gives the binary encoding back.
func TestDecodeEPC_TDS23AnnexE3Examples(t *testing.T) {
	for _, tc := range []struct {
		hex, scheme, urn, tag, element, key, serial string
		filter                                      int
		encoded                                     string // what EncodeEPCTagURI gives, when not the annex's hex
	}{
		{"3C76451FD400C0E680003039", "CPI-96", "urn:epc:id:cpi:9521141.98765.12345", "urn:epc:tag:cpi-96:3.9521141.98765.12345",
			"(8010)952114198765(8011)12345", "952114198765", "12345", 3, ""},
		{"3D76451FD75411DEF6B4CC00000003039000", "CPI-var", "urn:epc:id:cpi:9521141.5PQ7%2FZ43.12345", "urn:epc:tag:cpi-var:3.9521141.5PQ7%2FZ43.12345",
			"(8010)95211415PQ7/Z43(8011)12345", "95211415PQ7/Z43", "12345", 3, ""},
		{"3F76451FD612640000019907", "SGCN-96", "urn:epc:id:sgcn:9521141.67890.04711", "urn:epc:tag:sgcn-96:3.9521141.67890.04711",
			"(255)952114167890904711", "9521141678909", "04711", 3, ""},
		// The annex writes this 146-bit tag in 37 hexadecimal characters (padded to a hex digit); the encoder pads to a
		// 16-bit word as readers report EPC memory, so it adds three zero digits.
		{"3B0E0CF5E76C9047759AD00373DC7602E7200", "ADI-var", "urn:epc:id:adi:35962.PQ7VZ4.M37GXB92", "urn:epc:tag:adi-var:3.35962.PQ7VZ4.M37GXB92",
			"", "35962PQ7VZ4", "M37GXB92", 3, "3B0E0CF5E76C9047759AD00373DC7602E7200000"},
		{"4076451FD40C0E40820000000F54", "ITIP-110", "urn:epc:id:itip:9521141.012345.01.02.981", "urn:epc:tag:itip-110:3.9521141.012345.01.02.981",
			"(8006)095211411234540102(21)981", "095211411234540102", "981", 3, ""},
		{"4176451FD40C0E4082DBDD8B36600000000000000000000000000000", "ITIP-212", "urn:epc:id:itip:9521141.012345.01.02.mw133",
			"urn:epc:tag:itip-212:3.9521141.012345.01.02.mw133", "(8006)095211411234540102(21)mw133", "095211411234540102", "mw133", 3, ""},
	} {
		got, err := DecodeEPC(tc.hex)
		if err != nil || got.Scheme != tc.scheme || got.URN != tc.urn || got.TagURI != tc.tag || got.ElementString != tc.element ||
			got.Key != tc.key || got.Serial != tc.serial || got.Filter != tc.filter {
			t.Errorf("%s: %+v %v", tc.scheme, got, err)
		}
		want := tc.encoded
		if want == "" {
			want = tc.hex
		}
		if h, err := EncodeEPCTagURI(tc.tag); err != nil || h != want {
			t.Errorf("%s: encoding %s gives %s %v, want %s", tc.scheme, tc.tag, h, err, want)
		}
		if again, err := DecodeEPC(want); err != nil || again.TagURI != tc.tag {
			t.Errorf("%s: the encoder's padded form decodes to %+v %v", tc.scheme, again, err)
		}
	}
}

// The examples of TDS 2.3 sections 6.3 and 7, which give pure identity URIs and element strings but no binary
// encoding: each is encoded with a filter value and decoded back to the same URI and element string.
func TestDecodeEPC_TDS23URIExamples(t *testing.T) {
	for _, tc := range []struct {
		section, tag, urn, element string
	}{
		// 6.3.9, page 64: both CPI examples; the alphanumeric one only fits CPI-var.
		{"6.3.9", "urn:epc:tag:cpi-var:0.9521141.123ABC.123456789", "urn:epc:id:cpi:9521141.123ABC.123456789", "(8010)9521141123ABC(8011)123456789"},
		{"6.3.9", "urn:epc:tag:cpi-96:0.9521141.123456.123456789", "urn:epc:id:cpi:9521141.123456.123456789", "(8010)9521141123456(8011)123456789"},
		{"6.3.9", "urn:epc:tag:cpi-var:0.9521141.123456.123456789", "urn:epc:id:cpi:9521141.123456.123456789", "(8010)9521141123456(8011)123456789"},
		// 7.11, page 96.
		{"7.11", "urn:epc:tag:cpi-var:1.9521141.5PQ7%2FZ43.12345", "urn:epc:id:cpi:9521141.5PQ7%2FZ43.12345", "(8010)95211415PQ7/Z43(8011)12345"},
		// 6.3.10, page 64 (its element string worked out with the check digit formula of 7.12: GCN 4012345678901).
		{"6.3.10", "urn:epc:tag:sgcn-96:0.4012345.67890.04711", "urn:epc:id:sgcn:4012345.67890.04711", "(255)401234567890104711"},
		// 7.12, page 97.
		{"7.12", "urn:epc:tag:sgcn-96:0.9521141.67890.04711", "urn:epc:id:sgcn:9521141.67890.04711", "(255)952114167890904711"},
		// 6.3.13, page 66, as ITIP-110 and ITIP-212.
		{"6.3.13", "urn:epc:tag:itip-110:0.9521141.012345.01.02.987", "urn:epc:id:itip:9521141.012345.01.02.987", "(8006)095211411234540102(21)987"},
		{"6.3.13", "urn:epc:tag:itip-212:0.9521141.012345.01.02.987", "urn:epc:id:itip:9521141.012345.01.02.987", "(8006)095211411234540102(21)987"},
		// 7.15, page 102: the slash of the serial is escaped in the URI and not in the element string.
		{"7.15", "urn:epc:tag:itip-212:0.9521141.012345.04.04.32a%2Fb", "urn:epc:id:itip:9521141.012345.04.04.32a%2Fb", "(8006)095211411234540404(21)32a/b"},
		// 6.3.18, page 71: a CAGE code with no part number, and a DoDAAC with one.
		{"6.3.18", "urn:epc:tag:adi-var:0.2S194..12345678901", "urn:epc:id:adi:2S194..12345678901", ""},
		{"6.3.18", "urn:epc:tag:adi-var:0.W81X9C.3KL984PX1.2WMA52", "urn:epc:id:adi:W81X9C.3KL984PX1.2WMA52", ""},
	} {
		h, err := EncodeEPCTagURI(tc.tag)
		if err != nil {
			t.Errorf("%s %s: %v", tc.section, tc.tag, err)
			continue
		}
		got, err := DecodeEPC(h)
		if err != nil || got.URN != tc.urn || got.TagURI != tc.tag || got.ElementString != tc.element {
			t.Errorf("%s %s: %+v %v", tc.section, h, got, err)
		}
	}
}

// bitString lays a tag out field by field, as the coding tables give it.
type bitString struct{ s strings.Builder }

func (b *bitString) n(v uint64, width int) *bitString {
	fmt.Fprintf(&b.s, "%0*b", width, v)
	return b
}

func (b *bitString) raw(bits string) *bitString { b.s.WriteString(bits); return b }

// six writes the characters of TDS table G-1 (the low six bits of each ASCII code) and the 000000 terminator.
func (b *bitString) six(s string) *bitString {
	for i := 0; i < len(s); i++ {
		fmt.Fprintf(&b.s, "%06b", s[i]&0x3F)
	}
	b.s.WriteString("000000")
	return b
}

// seven writes 7-bit ASCII characters into a field of n characters, zero filled.
func (b *bitString) seven(s string, n int) *bitString {
	for i := 0; i < n; i++ {
		c := byte(0)
		if i < len(s) {
			c = s[i]
		}
		fmt.Fprintf(&b.s, "%07b", c)
	}
	return b
}

// sixRaw writes 6-bit characters with no terminator, as the CAGE code or DoDAAC of an ADI.
func (b *bitString) sixRaw(s string) *bitString {
	for i := 0; i < len(s); i++ {
		fmt.Fprintf(&b.s, "%06b", s[i]&0x3F)
	}
	return b
}

func (b *bitString) len() int { return b.s.Len() }

// hex pads the bits with zeros to a whole 16-bit word.
func (b *bitString) hex() string {
	s := b.s.String()
	s += strings.Repeat("0", (16-len(s)%16)%16)
	v, _ := new(big.Int).SetString(s, 2)
	return fmt.Sprintf("%0*X", len(s)/4, v)
}

func bits() *bitString { return &bitString{} }

// Vectors laid out by hand from the coding tables of TDS 2.3 where annex E has none: the first and last partition of
// each scheme with the largest values its fields hold, the longest variable-length strings and the shortest tags. The
// bits come from the tables (header, filter, partition, field widths) through bitString, not through the encoder, so
// encoder and decoder are both checked against them.
func TestDecodeEPC_TDS23DerivedVectors(t *testing.T) {
	adiPart := "ABCDEFGH-JKLMN/PQRSTUVWXYZ012345"      // 32 characters, table 14-30 notes
	adiSerial := "#" + "0123456789-ABCDEFGHIJKLMNOPQR" // "#" first and 29 more: 30 characters
	for _, tc := range []struct {
		name, tag string
		laid      *bitString
		wantBits  int
		urn       string
	}{
		// Table 14-25 with partition 0 of table 14-34: a 12-digit prefix in 40 bits, a part reference of at most 3
		// digits in 11 bits, the 31-bit serial at its largest.
		{"CPI-96 partition 0", "urn:epc:tag:cpi-96:0.952114100000.999.2147483647",
			bits().raw("00111100").n(0, 3).n(0, 3).n(952114100000, 40).n(999, 11).n(2147483647, 31), 96, "urn:epc:id:cpi:952114100000.999.2147483647"},
		// Partition 6: a 6-digit prefix in 20 bits, 9 digits of part reference in 31 bits.
		{"CPI-96 partition 6", "urn:epc:tag:cpi-96:7.952114.999999999.0",
			bits().raw("00111100").n(7, 3).n(6, 3).n(952114, 20).n(999999999, 31).n(0, 31), 96, "urn:epc:id:cpi:952114.999999999.0"},
		// Table 14-26 with partition 6 of table 14-35: the longest part reference, 24 characters (150 bits with the
		// terminator), every character of table G-1 kind, and the largest 12-digit serial: 224 bits, the most CPI-var has.
		{"CPI-var longest", "urn:epc:tag:cpi-var:0.952114.%23-%2F0123456789ABCDEFGHIJK.999999999999",
			bits().raw("00111101").n(0, 3).n(6, 3).n(952114, 20).six("#-/0123456789ABCDEFGHIJK").n(999999999999, 40), 224,
			"urn:epc:id:cpi:952114.%23-%2F0123456789ABCDEFGHIJK.999999999999"},
		// Partition 0 with a one-character part reference and serial 0: 106 bits.
		{"CPI-var shortest at partition 0", "urn:epc:tag:cpi-var:2.952114100000.Z.0",
			bits().raw("00111101").n(2, 3).n(0, 3).n(952114100000, 40).six("Z").n(0, 40), 106, "urn:epc:id:cpi:952114100000.Z.0"},
		// Table 14-40 with partition 0 of table 14-39: no coupon reference (one zero bit), and 12 zeros of serial kept
		// by the numeric string method (14.3.6): the field holds 1000000000000.
		{"SGCN-96 partition 0", "urn:epc:tag:sgcn-96:0.952114167890..000000000000",
			bits().raw("00111111").n(0, 3).n(0, 3).n(952114167890, 40).n(0, 1).n(1000000000000, 41), 96, "urn:epc:id:sgcn:952114167890..000000000000"},
		// Partition 6 with the largest coupon reference and serial: the field holds 1999999999999.
		{"SGCN-96 partition 6", "urn:epc:tag:sgcn-96:5.952114.999999.999999999999",
			bits().raw("00111111").n(5, 3).n(6, 3).n(952114, 20).n(999999, 21).n(1999999999999, 41), 96, "urn:epc:id:sgcn:952114.999999.999999999999"},
		// Table 14-43 with partition 0 of table 14-42: piece and total at 99 (14.4.10: 2 digits in 7 bits), the 38-bit
		// serial at its largest, 274877906943.
		{"ITIP-110 partition 0", "urn:epc:tag:itip-110:0.952114112345.9.99.99.274877906943",
			bits().raw("01000000").n(0, 3).n(0, 3).n(952114112345, 40).n(9, 4).n(99, 7).n(99, 7).n(274877906943, 38), 110,
			"urn:epc:id:itip:952114112345.9.99.99.274877906943"},
		// Table 14-44 with partition 6: a 7-digit indicator and item reference, 20 serial characters that use every
		// escape of table A-1 and a dot (the serial is the last field, so a dot in it is not a separator).
		{"ITIP-212 partition 6", "urn:epc:tag:itip-212:1.952114.1234567.00.00.a%22%25%26%2F%3C%3E%3F.!'()*+,-:;=",
			bits().raw("01000001").n(1, 3).n(6, 3).n(952114, 20).n(1234567, 24).n(0, 7).n(0, 7).seven(`a"%&/<>?.!'()*+,-:;=`, 20), 212,
			"urn:epc:id:itip:952114.1234567.00.00.a%22%25%26%2F%3C%3E%3F.!'()*+,-:;="},
		// Table 14-30 at its longest: filter 63, a 6-character DoDAAC, 32 characters of part number and 30 of serial
		// starting with "#": 8 + 6 + 36 + 198 + 186 = 434 bits.
		{"ADI-var longest", "urn:epc:tag:adi-var:63.W81X9C." + strings.ReplaceAll(adiPart, "/", "%2F") + ".%23" + adiSerial[1:],
			bits().raw("00111011").n(63, 6).sixRaw("W81X9C").six(adiPart).six(adiSerial), 434,
			"urn:epc:id:adi:W81X9C." + strings.ReplaceAll(adiPart, "/", "%2F") + ".%23" + adiSerial[1:]},
		// At its shortest: a 5-character CAGE code after 100000 (14.3.7), no part number, one serial character:
		// 68 bits. The CAGE code 2S194 by hand from table G-1: 2 = 110010, S = 010011, 1 = 110001, 9 = 111001,
		// 4 = 110100.
		{"ADI-var shortest", "urn:epc:tag:adi-var:0.2S194..1",
			bits().raw("00111011").n(0, 6).raw("100000").raw("110010010011110001111001110100").raw("000000").six("1"), 68,
			"urn:epc:id:adi:2S194..1"},
	} {
		laid := tc.laid
		if laid.len() != tc.wantBits {
			t.Fatalf("%s: laid out %d bits, the table gives %d", tc.name, laid.len(), tc.wantBits)
		}
		want := laid.hex()
		h, err := EncodeEPCTagURI(tc.tag)
		if err != nil || h != want {
			t.Errorf("%s: encoder %s %v, laid out %s", tc.name, h, err, want)
		}
		got, err := DecodeEPC(want)
		if err != nil || got.TagURI != tc.tag || got.URN != tc.urn {
			t.Errorf("%s: %+v %v", tc.name, got, err)
		}
		// Unpadded, rounded up to a hexadecimal digit, the tag reads the same.
		if again, err := DecodeEPC(want[:(tc.wantBits+3)/4]); err != nil || again.TagURI != tc.tag {
			t.Errorf("%s unpadded: %+v %v", tc.name, again, err)
		}
	}
}

func wantEPCInvalid(t *testing.T, name, hex string) {
	t.Helper()
	got, err := DecodeEPC(hex)
	var de *Error
	if err == nil || !errors.As(err, &de) || de.Code != CodeEPCInvalid {
		t.Errorf("%s: %s decoded as %+v, %v", name, hex, got, err)
	}
}

// Tags that break a rule of the coding tables are refused with WMS-EPC-INVALID.
func TestDecodeEPC_TDS23Refusals(t *testing.T) {
	cpi96 := func(part, cpBits int, cp uint64, refBits int, ref uint64) string {
		return bits().raw("00111100").n(0, 3).n(uint64(part), 3).n(cp, cpBits).n(ref, refBits).n(1, 31).hex()
	}
	cpiVar := func(part, cpBits int, cp uint64, ref string, serial uint64) *bitString {
		return bits().raw("00111101").n(0, 3).n(uint64(part), 3).n(cp, cpBits).six(ref).n(serial, 40)
	}
	sgcn := func(part, cpBits int, refBits int, ref uint64, serialField uint64) string {
		return bits().raw("00111111").n(0, 3).n(uint64(part), 3).n(0, cpBits).n(ref, refBits).n(serialField, 41).hex()
	}
	itip110 := func(piece, total uint64) string {
		return bits().raw("01000000").n(0, 3).n(5, 3).n(9521141, 24).n(12345, 20).n(piece, 7).n(total, 7).n(1, 38).hex()
	}
	adi := func(cage string, part, serial string) *bitString {
		b := bits().raw("00111011").n(0, 6)
		if len(cage) == 5 {
			b.raw("100000")
		}
		for i := 0; i < len(cage); i++ {
			b.n(uint64(cage[i]&0x3F), 6)
		}
		return b.six(part).six(serial)
	}
	for name, hex := range map[string]string{
		// CPI-96: table 14-34 allows 3 digits of part reference with a 12-digit prefix (TDT 2.2: at most 999), though
		// the 11 bits hold up to 2047.
		"CPI-96 part reference of 4 digits at partition 0":  cpi96(0, 40, 952114100000, 11, 1000),
		"CPI-96 part reference of 10 digits at partition 6": cpi96(6, 20, 952114, 31, 1000000000),
		"CPI-96 partition 7":                  cpi96(7, 20, 952114, 31, 1),
		"CPI-96 company prefix of 13 digits":  cpi96(0, 40, 1000000000000, 11, 1),
		"CPI-96 of 23 hexadecimal characters": cpi96(5, 24, 9521141, 27, 98765)[:23],
		"CPI-96 with bits after it":           cpi96(5, 24, 9521141, 27, 98765) + "1",
		// CPI-var: the terminator, the character set, the most characters per partition and the 12-digit serial.
		"CPI-var 19 characters at partition 0":   cpiVar(0, 40, 952114100000, strings.Repeat("A", 19), 1).hex(),
		"CPI-var 25 characters at partition 6":   cpiVar(6, 20, 952114, strings.Repeat("A", 25), 1).hex(),
		"CPI-var empty part reference":           bits().raw("00111101").n(0, 3).n(5, 3).n(9521141, 24).raw("000000").n(1, 40).hex(),
		"CPI-var no terminator before the end":   bits().raw("00111101").n(0, 3).n(5, 3).n(9521141, 24).raw(strings.Repeat("000001", 10)).hex(),
		"CPI-var serial cut short":               cpiVar(5, 24, 9521141, "AB", 1).hex()[:20],
		"CPI-var 6-bit value 111010 (':')":       bits().raw("00111101").n(0, 3).n(5, 3).n(9521141, 24).raw("000001111010000000").n(1, 40).hex(),
		"CPI-var 6-bit value 011011 (no char)":   bits().raw("00111101").n(0, 3).n(5, 3).n(9521141, 24).raw("011011000000").n(1, 40).hex(),
		"CPI-var serial of 13 digits":            cpiVar(5, 24, 9521141, "AB", 1000000000000).hex(),
		"CPI-var non-zero bits after the serial": cpiVar(5, 24, 9521141, "ABC", 1).raw("1").hex(), // 102 bits, then a one
		"CPI-var padded past a 16-bit word":      cpiVar(5, 24, 9521141, "AB", 1).hex() + "0000",
		// SGCN-96: the numeric string must start with 1 and hold a digit; the coupon reference must fit its digits.
		"SGCN-96 serial field 1 (empty serial)":           sgcn(5, 24, 17, 67890, 1),
		"SGCN-96 serial field 2000000000000":              sgcn(5, 24, 17, 67890, 2000000000000),
		"SGCN-96 serial field 0":                          sgcn(5, 24, 17, 67890, 0),
		"SGCN-96 coupon reference of 7 digits":            sgcn(6, 20, 21, 1000000, 104711),
		"SGCN-96 coupon reference bit set at partition 0": sgcn(0, 40, 1, 1, 104711),
		// ITIP: piece and total are 2-digit fixed width integers (14.4.10), the lengths are fixed.
		"ITIP-110 piece 100":                    itip110(100, 2),
		"ITIP-110 total 127":                    itip110(1, 127),
		"ITIP-110 as 24 hexadecimal characters": itip110(1, 2)[:24],
		"ITIP-212 as 28 hexadecimal characters": "4176451FD40C0E4082DBDD8B3660",
		"ITIP-212 serial character outside A-1": bits().raw("01000001").n(0, 3).n(5, 3).n(9521141, 24).n(12345, 20).n(1, 7).n(2, 7).seven(" ", 20).hex(),
		"ITIP-212 empty serial":                 bits().raw("01000001").n(0, 3).n(5, 3).n(9521141, 24).n(12345, 20).n(1, 7).n(2, 7).seven("", 20).hex(),
		// ADI-var: the CAGE characters, the "#" rule, the most characters and the terminators.
		"ADI-var CAGE with I":                  adi("2SI94", "", "1").hex(),
		"ADI-var CAGE with O":                  adi("W81XOC", "", "1").hex(),
		"ADI-var CAGE first segment 100001":    bits().raw("00111011").n(0, 6).raw("100001").raw("110010010011110001111001110100").six("").six("1").hex(),
		"ADI-var part number of 33":            adi("2S194", strings.Repeat("A", 33), "1").hex(),
		"ADI-var serial of 31":                 adi("2S194", "", strings.Repeat("1", 31)).hex(),
		"ADI-var empty serial":                 adi("2S194", "PN", "").hex(),
		"ADI-var # in the part number":         adi("2S194", "#PN", "1").hex(),
		"ADI-var # inside the serial":          adi("2S194", "PN", "1#2").hex(),
		"ADI-var serial # alone":               adi("2S194", "PN", "#").hex(),
		"ADI-var no serial terminator":         bits().raw("00111011").n(0, 6).raw("100000").raw("110010010011110001111001110100").six("").raw("110001110001").hex()[:17],
		"ADI-var non-zero bits after the tag":  adi("2S194", "", "1").raw("01").hex(),
		"ADI-var padded past a 16-bit word":    adi("2S194", "", "1").hex() + "0000",
		"ADI-var 6-bit value 100001 (no char)": bits().raw("00111011").n(0, 6).raw("100000").raw("110010010011110001111001110100").six("").raw("100001000000").hex(),
	} {
		wantEPCInvalid(t, name, hex)
	}
	// The helpers lay out tags that decode when their fields are good.
	if _, err := DecodeEPC(sgcn(5, 24, 17, 67890, 104711)); err != nil {
		t.Fatalf("SGCN helper: %v", err)
	}
	if _, err := DecodeEPC(itip110(99, 99)); err != nil {
		t.Fatalf("ITIP piece and total 99: %v", err)
	}
	if _, err := DecodeEPC(adi("2S194", "", "#1").hex()); err != nil {
		t.Fatalf("ADI serial starting with #: %v", err)
	}
}

// Tag URIs the encoder refuses: each breaks the grammar of TDS 2.3 sections 6.3 and 12.4 or a limit of a coding table.
func TestEncodeEPCTagURI_Refusals(t *testing.T) {
	for _, uri := range []string{
		"urn:epc:id:cpi:9521141.98765.12345",                                     // a pure identity URI, not a tag URI
		"urn:epc:tag:sgtin-96:3.0614141.812345.6789",                             // not one of these schemes
		"urn:epc:tag:cpi-96:8.9521141.98765.12345",                               // filter of 3 bits
		"urn:epc:tag:cpi-96:03.9521141.98765.12345",                              // leading zero in the filter
		"urn:epc:tag:cpi-96:3.952114.1000000000.1",                               // 10 digits of part reference at partition 6
		"urn:epc:tag:cpi-96:3.9521141.098765.12345",                              // part reference with a leading zero (CPI-96 has none)
		"urn:epc:tag:cpi-96:3.9521141.ABC.12345",                                 // letters do not fit CPI-96
		"urn:epc:tag:cpi-96:3.9521141.98765.2147483648",                          // serial of 32 bits
		"urn:epc:tag:cpi-96:3.95211.98765.12345",                                 // 5-digit company prefix
		"urn:epc:tag:cpi-var:3.9521141.5PQ7/Z43.12345",                           // "/" unescaped
		"urn:epc:tag:cpi-var:3.9521141.5pq7.12345",                               // lower case
		"urn:epc:tag:cpi-var:3.9521141.5PQ%25.12345",                             // escape outside table G-1
		"urn:epc:tag:cpi-var:3.9521141..12345",                                   // empty part reference
		"urn:epc:tag:cpi-var:3.952114100000." + strings.Repeat("A", 19) + ".1",   // 19 characters at partition 0
		"urn:epc:tag:cpi-var:3.9521141.AB.1000000000000",                         // 13-digit serial
		"urn:epc:tag:cpi-var:3.9521141.AB",                                       // missing serial
		"urn:epc:tag:sgcn-96:3.9521141.6789.04711",                               // coupon reference of the wrong width
		"urn:epc:tag:sgcn-96:3.9521141.67890.0471100000000",                      // 13-digit serial
		"urn:epc:tag:sgcn-96:3.9521141.67890.",                                   // serial is mandatory
		"urn:epc:tag:itip-110:3.9521141.012345.1.02.981",                         // piece is 2 digits
		"urn:epc:tag:itip-110:3.9521141.012345.01.02.0981",                       // ITIP-110 serial without leading zeros
		"urn:epc:tag:itip-110:3.9521141.012345.01.02.274877906944",               // 2^38
		"urn:epc:tag:itip-110:3.9521141.12345.01.02.981",                         // reference of the wrong width
		"urn:epc:tag:itip-212:3.9521141.012345.01.02." + strings.Repeat("a", 21), // 21 characters
		"urn:epc:tag:itip-212:3.9521141.012345.01.02.a/b",                        // "/" unescaped
		"urn:epc:tag:itip-212:3.9521141.012345.01.02.a%20b",                      // space is not in table A-1
		"urn:epc:tag:itip-212:3.9521141.012345.01.02.a%2",                        // cut escape
		"urn:epc:tag:adi-var:64.35962.PQ7VZ4.M37GXB92",                           // filter of 6 bits
		"urn:epc:tag:adi-var:3.3596.PQ7VZ4.M37GXB92",                             // 4-character CAGE
		"urn:epc:tag:adi-var:3.3596I.PQ7VZ4.M37GXB92",                            // I in a CAGE code
		"urn:epc:tag:adi-var:3.35962.PQ7%23Z4.M37GXB92",                          // # in the part number
		"urn:epc:tag:adi-var:3.35962.PQ7VZ4.M37%23XB92",                          // # inside the serial
		"urn:epc:tag:adi-var:3.35962.PQ7VZ4.%23",                                 // # with nothing after it
		"urn:epc:tag:adi-var:3.35962.PQ7VZ4.",                                    // serial is mandatory
		"urn:epc:tag:adi-var:3.35962." + strings.Repeat("A", 33) + ".1",          // part number of 33
		"urn:epc:tag:adi-var:3.35962.." + strings.Repeat("1", 31),                // serial of 31
	} {
		if h, err := EncodeEPCTagURI(uri); err == nil {
			t.Errorf("%s encoded as %s", uri, h)
		} else if de := (*Error)(nil); !errors.As(err, &de) || de.Code != CodeEPCInvalid {
			t.Errorf("%s: %v", uri, err)
		}
	}
}

// Random tag URIs of every new scheme encode, decode and give the same tag URI back.
func TestEncodeEPCTagURI_RoundTrips(t *testing.T) {
	r := rand.New(rand.NewPCG(32, 23))
	digits := func(n int) string {
		var sb strings.Builder
		for i := 0; i < n; i++ {
			sb.WriteByte(byte('0' + r.IntN(10)))
		}
		return sb.String()
	}
	number := func(limit uint64) string { return fmt.Sprint(r.Uint64N(limit)) }
	pick := func(set string, n int) string {
		var sb strings.Builder
		for i := 0; i < n; i++ {
			sb.WriteByte(set[r.IntN(len(set))])
		}
		return sb.String()
	}
	sixBit := "#-/0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ"
	a1 := `0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz!"%&'()*+,-./:;<=>?_`
	cageSet := "0123456789ABCDEFGHJKLMNPQRSTUVWXYZ"
	for i := 0; i < 2000; i++ {
		part := r.IntN(7)
		l := 12 - part
		var uri string
		switch i % 6 {
		case 0:
			k := cpi96Partitions[part].refDigits
			uri = fmt.Sprintf("urn:epc:tag:cpi-96:%d.%s.%s.%s", r.IntN(8), digits(l), number(pow10(k)), number(1<<31))
		case 1:
			uri = fmt.Sprintf("urn:epc:tag:cpi-var:%d.%s.%s.%s", r.IntN(8), digits(l), sixBitEscape(pick(sixBit, 1+r.IntN(cpiVarPartitions[part].refDigits))), number(1e12))
		case 2:
			uri = fmt.Sprintf("urn:epc:tag:sgcn-96:%d.%s.%s.%s", r.IntN(8), digits(l), digits(sgcnPartitions[part].refDigits), digits(1+r.IntN(12)))
		case 3:
			uri = fmt.Sprintf("urn:epc:tag:itip-110:%d.%s.%s.%s.%s.%s", r.IntN(8), digits(l), digits(itipPartitions[part].refDigits), digits(2), digits(2), number(1<<38))
		case 4:
			uri = fmt.Sprintf("urn:epc:tag:itip-212:%d.%s.%s.%s.%s.%s", r.IntN(8), digits(l), digits(itipPartitions[part].refDigits), digits(2), digits(2), urnEscape(pick(a1, 1+r.IntN(20))))
		case 5:
			serial := pick(sixBit[1:], 1+r.IntN(29))
			if r.IntN(2) == 0 {
				serial = "#" + serial
			}
			uri = fmt.Sprintf("urn:epc:tag:adi-var:%d.%s.%s.%s", r.IntN(64), pick(cageSet, 5+r.IntN(2)), sixBitEscape(pick(sixBit[1:], r.IntN(33))), sixBitEscape(serial))
		}
		h, err := EncodeEPCTagURI(uri)
		if err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		got, err := DecodeEPC(h)
		if err != nil || got.TagURI != uri {
			t.Fatalf("%s -> %s -> %+v %v", uri, h, got, err)
		}
		again, err := EncodeEPCTagURI(got.TagURI)
		if err != nil || again != h {
			t.Fatalf("%s re-encoded as %s %v, first %s", got.TagURI, again, err, h)
		}
	}
}

func pow10(k int) uint64 {
	v := uint64(1)
	for i := 0; i < k; i++ {
		v *= 10
	}
	return v
}

// Table 14-1 of TDS 2.3 (pages 149 to 152), transcribed here apart from the code: every header value and its coding
// scheme. The test walks all 256 values: a header the warehouse decodes gives only its own scheme, whatever bits
// follow, and every other header is refused with WMS-EPC-INVALID naming what the table says it is.
func TestDecodeEPC_WalksTheTDS23HeaderTable(t *testing.T) {
	table := map[int]string{0x00: "Unprogrammed Tag"}
	for h := 0x01; h <= 0x2B; h++ {
		table[h] = "Reserved for Future Use"
	}
	for h, name := range map[int]string{
		0x2C: "GDTI-96", 0x2D: "GSRN-96", 0x2E: "GSRNP-96", 0x2F: "USDoD-96", 0x30: "SGTIN-96", 0x31: "SSCC-96", 0x32: "SGLN-96",
		0x33: "GRAI-96", 0x34: "GIAI-96", 0x35: "GID-96", 0x36: "SGTIN-198", 0x37: "GRAI-170", 0x38: "GIAI-202", 0x39: "SGLN-195",
		0x3A: "GDTI-113", 0x3B: "ADI-var", 0x3C: "CPI-96", 0x3D: "CPI-var", 0x3E: "GDTI-174", 0x3F: "SGCN-96", 0x40: "ITIP-110",
		0x41: "ITIP-212", 0xE2: "PERMANENTLY RESERVED", 0xE6: "CPI++", 0xE7: "GSRN++", 0xE8: "GSRNP++", 0xE9: "SGLN++",
		0xEA: "GDTI++", 0xEB: "GRAI++", 0xEC: "SGCN++", 0xED: "ITIP++", 0xEE: "GIAI++", 0xEF: "SSCC++", 0xF0: "CPI+", 0xF1: "GRAI+",
		0xF2: "SGLN+", 0xF3: "ITIP+", 0xF4: "GSRN+", 0xF5: "GSRNP+", 0xF6: "GDTI+", 0xF7: "SGTIN+", 0xF8: "SGCN+", 0xF9: "SSCC+",
		0xFA: "GIAI+", 0xFB: "DSGTIN+", 0xFC: "DSGTIN++", 0xFD: "SGTIN++", 0xFE: "Reserved for Future Use",
		0xFF: "Reserved for Future Use (expressly reserved for headers longer than 8 bits)",
	} {
		table[h] = name
	}
	for h := 0x42; h <= 0xE1; h++ {
		table[h] = "Reserved for Future Use"
	}
	for h := 0xE3; h <= 0xE5; h++ {
		table[h] = "Reserved for Future Use"
	}
	if len(table) != 256 {
		t.Fatalf("the transcription has %d header values", len(table))
	}
	// A valid tag of each header the warehouse decodes.
	valid := map[int]string{
		0x2C: "2C74257BF460720000000190", 0x2D: mustEPC(t, 0x2D, "0614141", "0123456789", nil), 0x2E: "2E" + mustEPC(t, 0x2D, "0614141", "0123456789", nil)[2:],
		0x30: "3074257BF7194E4000001A85", 0x31: mustEPC(t, 0x31, "0614141", "0123456789", nil), 0x32: mustEPC(t, 0x32, "0614141", "12345", nil),
		0x33: mustEPC(t, 0x33, "0614141", "12345", big.NewInt(1)), 0x34: mustEPC(t, 0x34, "0614141", "123456789012345678", nil),
		0x35: "3500E86F8000A9E000000586",                             // annex E.3 page 364: urn:epc:id:gid:952056.2718.1414
		0x36: "3666C4409047E159B2C2BF100000000000000000000000000000", // annex E.3 page 356: SGTIN-198
		0x37: "3774257BF40C0E59B2C2BF1000000000000000000000", 0x38: "3874257BF58B266D1AB460C00000000000000000000000000000",
		0x39: "3974257BF46072D5B37700000000000000000000000000000000", 0x3E: "3E74257BF46072C183670D1B80000000000000000000",
		0x3B: "3B0E0CF5E76C9047759AD00373DC7602E7200", 0x3C: "3C76451FD400C0E680003039", 0x3D: "3D76451FD75411DEF6B4CC00000003039000",
		0x3F: "3F76451FD612640000019907", 0x40: "4076451FD40C0E40820000000F54", 0x41: "4176451FD40C0E4082DBDD8B36600000000000000000000000000000",
	}
	r := rand.New(rand.NewPCG(14, 1))
	for h := 0; h < 256; h++ {
		name := table[h]
		if hex, ok := valid[h]; ok {
			got, err := DecodeEPC(hex)
			if err != nil || got.Scheme != name {
				t.Errorf("header 0x%02X (%s): %+v %v", h, name, got, err)
			}
		}
		// Whatever bits follow, of any length a reader reports, a header decodes as its own scheme or not at all.
		for _, hexLen := range []int{17, 20, 24, 28, 37, 44, 52, 56, 64, 112} {
			for try := 0; try < 40; try++ {
				body := make([]byte, hexLen-2)
				for i := range body {
					body[i] = "0123456789ABCDEF"[r.IntN(16)]
				}
				// Half the tries end in zero bits, as short and padded tags do.
				if try%2 == 1 {
					keep := r.IntN(len(body) + 1)
					for i := keep; i < len(body); i++ {
						body[i] = '0'
					}
				}
				in := fmt.Sprintf("%02X%s", h, body)
				got, err := DecodeEPC(in)
				if err == nil && got.Scheme != name {
					t.Fatalf("header 0x%02X (%s): %s decoded as %s", h, name, in, got.Scheme)
				}
				var de *Error
				if err != nil && (!errors.As(err, &de) || de.Code != CodeEPCInvalid) {
					t.Fatalf("header 0x%02X: %v", h, err)
				}
				if _, ok := valid[h]; !ok {
					if err == nil {
						t.Fatalf("header 0x%02X (%s) decoded %s", h, name, in)
					}
					want := name
					switch {
					case strings.HasPrefix(name, "Reserved"):
						want = "reserved"
					case h == 0x00:
						want = "unprogrammed"
					case h == 0xE2:
						want = "permanently reserved"
					case h == 0x3A:
						want = "GDTI-113"
					}
					if !strings.Contains(de.Detail, want) {
						t.Fatalf("header 0x%02X: %q does not say %q", h, de.Detail, want)
					}
				}
			}
		}
	}
}
