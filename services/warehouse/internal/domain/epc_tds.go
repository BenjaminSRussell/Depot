package domain

import (
	"fmt"
	"math/big"
	"strconv"
	"strings"
)

// The CPI, SGCN, ITIP and ADI EPC schemes (#32), written from GS1 EPC Tag Data Standard (TDS) Release 2.3 (ratified
// Oct 2025): header values from table 14-1, coding tables 14-25 (CPI-96), 14-26 (CPI-var), 14-40 (SGCN-96), 14-43
// (ITIP-110), 14-44 (ITIP-212) and 14-30 (ADI-var), partition tables 14-34, 14-35, 14-39 and 14-42, the coding
// methods of sections 14.3 and 14.4, the URI grammars of sections 6.3 and 12.4, and the element strings of sections
// 7.11, 7.12 and 7.15. Where a method alone would allow more than a table's character count row (the CPI-96 part
// reference, the CPI-var serial), the narrower limit is kept, as the TDT 2.2 definition files have it.

// Table 14-1 of TDS 2.3: what each header value is. A header this warehouse does not decode is refused naming it, so
// a tag is never read as another scheme. The schemes TDS 2.0 added (CPI+ to SGTIN++, E6 to FD) carry the GS1 key
// with encoding indicators and +AIDC data; they are not decoded.
func epcHeaderName(h int) string {
	switch {
	case h == 0x00:
		return "an unprogrammed tag"
	case h == 0x2F:
		return "USDoD-96"
	case h == 0x3A:
		return "GDTI-113 (deprecated as of TDS 1.9)"
	case h == 0xE2:
		return "permanently reserved (the first byte of TID memory)"
	case h == 0xFF:
		return "reserved for headers longer than 8 bits"
	case h >= 0xE6 && h <= 0xFD:
		return tds2Headers[h-0xE6]
	}
	if sc, ok := epcSchemes[h]; ok {
		return sc.name
	}
	if d, ok := tdsDecoders[h]; ok {
		return d.name
	}
	if h == gidHeader {
		return "GID-96"
	}
	return "reserved for future use"
}

var tds2Headers = [...]string{"CPI++", "GSRN++", "GSRNP++", "SGLN++", "GDTI++", "GRAI++", "SGCN++", "ITIP++", "GIAI++",
	"SSCC++", "CPI+", "GRAI+", "SGLN+", "ITIP+", "GSRN+", "GSRNP+", "GDTI+", "SGTIN+", "SGCN+", "SSCC+", "GIAI+", "DSGTIN+",
	"DSGTIN++", "SGTIN++"}

func refuseHeader(h int) error {
	name := epcHeaderName(h)
	if strings.HasPrefix(name, "reserved") || strings.HasPrefix(name, "permanently") {
		return epcErr("EPC header 0x%02X is %s in GS1 TDS 2.3 table 14-1, not a scheme", h, name)
	}
	return epcErr("EPC header 0x%02X is %s, a scheme this warehouse does not decode", h, name)
}

// tdsDecoder decodes one of the schemes below. bits is the scheme's fixed length, 0 for a variable-length one.
type tdsDecoder struct {
	name   string
	bits   int
	decode func(b epcBits) (EPC, int, error) // the EPC and the number of bits it used
}

var tdsDecoders = map[int]tdsDecoder{
	0x3C: {"CPI-96", 96, decodeCPI96},
	0x3D: {"CPI-var", 0, decodeCPIVar},
	0x3F: {"SGCN-96", 96, decodeSGCN96},
	0x40: {"ITIP-110", 110, func(b epcBits) (EPC, int, error) { return decodeITIP(b, false) }},
	0x41: {"ITIP-212", 212, func(b epcBits) (EPC, int, error) { return decodeITIP(b, true) }},
	0x3B: {"ADI-var", 0, decodeADIVar},
}

// Partition tables. For CPI-96 the reference columns are its bits and its most digits (table 14-34); for CPI-var they
// are the most bits of the 6-bit part reference with its terminator and its most characters (table 14-35).
var (
	cpi96Partitions  = []epcPartition{{40, 12, 11, 3}, {37, 11, 14, 4}, {34, 10, 17, 5}, {30, 9, 21, 6}, {27, 8, 24, 7}, {24, 7, 27, 8}, {20, 6, 31, 9}}
	cpiVarPartitions = []epcPartition{{40, 12, 114, 18}, {37, 11, 120, 19}, {34, 10, 126, 20}, {30, 9, 132, 21}, {27, 8, 138, 22}, {24, 7, 144, 23}, {20, 6, 150, 24}}
	sgcnPartitions   = []epcPartition{{40, 12, 1, 0}, {37, 11, 4, 1}, {34, 10, 7, 2}, {30, 9, 11, 3}, {27, 8, 14, 4}, {24, 7, 17, 5}, {20, 6, 21, 6}}
	itipPartitions   = []epcPartition{{40, 12, 4, 1}, {37, 11, 7, 2}, {34, 10, 10, 3}, {30, 9, 14, 4}, {27, 8, 17, 5}, {24, 7, 20, 6}, {20, 6, 24, 7}}
)

const (
	cpiVarSerialMax = 999999999999 // table 14-26: "up to 12 digits" (AI 8011 is N..12)
	adiPartMax      = 32           // table 14-30 notes: 0 to 32 characters of part number
	adiSerialMax    = 30           // and 1 to 30 of serial
)

// decodeTDS decodes the hexadecimal of one of the schemes above. A fixed-length tag follows the length rule of the
// other schemes; a variable-length one may be followed by zero bits up to a whole 16-bit word (TDS 14.4 step 4).
func decodeTDS(d tdsDecoder, b epcBits, hexLen int) (EPC, error) {
	if d.bits > 0 {
		if err := checkEPCWidth(b, d.bits, hexLen); err != nil {
			return EPC{}, err
		}
	}
	out, used, err := d.decode(b)
	if err != nil {
		return EPC{}, err
	}
	if d.bits == 0 {
		padded := (used + 15) / 16 * 16
		if b.width > padded {
			return EPC{}, epcErr("the %d-bit %s is at most %d hexadecimal characters with its padding, this has %d", used, d.name, padded/4, hexLen)
		}
		if b.width > used && b.at(used, b.width-used).Sign() != 0 {
			return EPC{}, epcErr("the bits after the %d-bit %s are not zero", used, d.name)
		}
	}
	out.Scheme = d.name
	out.TagURI = tagURI(out)
	return out, nil
}

// epcCursor reads the fields of a tag one after another and fails when the tag ends first.
type epcCursor struct {
	b   epcBits
	pos int
}

func (c *epcCursor) take(n int) (*big.Int, error) {
	if c.pos+n > c.b.width {
		return nil, epcErr("the tag ends before its last field")
	}
	v := c.b.at(c.pos, n)
	c.pos += n
	return v, nil
}

// partitionHead reads the filter (3 bits), the partition and the company prefix, as every GS1 scheme here starts.
func (c *epcCursor) partitionHead(scheme string, table []epcPartition) (filter int, part int, p epcPartition, cp string, err error) {
	c.pos = 8
	f, err := c.take(3)
	if err != nil {
		return 0, 0, p, "", err
	}
	pv, err := c.take(3)
	if err != nil {
		return 0, 0, p, "", err
	}
	part = int(pv.Int64())
	if part >= len(table) {
		return 0, 0, p, "", epcErr("EPC partition %d is not defined for %s", part, scheme)
	}
	p = table[part]
	cv, err := c.take(p.cpBits)
	if err != nil {
		return 0, 0, p, "", err
	}
	cp, ok := zeroPad(cv, p.cpDigits)
	if !ok {
		return 0, 0, p, "", epcErr("the company prefix of the %s is out of range for its partition", scheme)
	}
	return int(f.Int64()), part, p, cp, nil
}

// decodeCPI96 follows table 14-25: an unpadded partition table (14.4.4) whose part reference has at most the digits
// of table 14-34, and a 31-bit serial.
func decodeCPI96(b epcBits) (EPC, int, error) {
	c := &epcCursor{b: b}
	filter, part, p, cp, err := c.partitionHead("CPI-96", cpi96Partitions)
	if err != nil {
		return EPC{}, 0, err
	}
	rv, _ := c.take(p.refBits)
	ref := rv.String()
	if len(ref) > p.refDigits {
		return EPC{}, 0, epcErr("the CPI-96 part reference %s has more than %d digits for a %d-digit company prefix", ref, p.refDigits, p.cpDigits)
	}
	sv, _ := c.take(31)
	return cpiEPC(filter, part, cp, ref, sv.String()), c.pos, nil
}

// decodeCPIVar follows table 14-26: the part reference in 6-bit characters ended by 000000 (14.4.9), then a 40-bit
// serial.
func decodeCPIVar(b epcBits) (EPC, int, error) {
	c := &epcCursor{b: b}
	filter, part, p, cp, err := c.partitionHead("CPI-var", cpiVarPartitions)
	if err != nil {
		return EPC{}, 0, err
	}
	ref, err := c.sixBitString("part reference", 1, p.refDigits, cpiChar)
	if err != nil {
		return EPC{}, 0, err
	}
	sv, err := c.take(40)
	if err != nil {
		return EPC{}, 0, err
	}
	if sv.Cmp(big.NewInt(cpiVarSerialMax)) > 0 {
		return EPC{}, 0, epcErr("the CPI-var serial %s has more than 12 digits", sv)
	}
	return cpiEPC(filter, part, cp, ref, sv.String()), c.pos, nil
}

func cpiEPC(filter, part int, cp, ref, serial string) EPC {
	return EPC{Filter: filter, Partition: part, CompanyPrefix: cp, Key: cp + ref, Serial: serial,
		URN:           fmt.Sprintf("urn:epc:id:cpi:%s.%s.%s", cp, sixBitEscape(ref), serial),
		ElementString: fmt.Sprintf("(8010)%s%s(8011)%s", cp, ref, serial)}
}

// decodeSGCN96 follows table 14-40: a partition table (14.4.3) and the serial as a numeric string (14.4.6), whose
// leading "1" keeps the serial's leading zeros.
func decodeSGCN96(b epcBits) (EPC, int, error) {
	c := &epcCursor{b: b}
	filter, part, p, cp, err := c.partitionHead("SGCN-96", sgcnPartitions)
	if err != nil {
		return EPC{}, 0, err
	}
	rv, _ := c.take(p.refBits)
	ref, ok := zeroPad(rv, p.refDigits)
	if !ok {
		return EPC{}, 0, epcErr("the coupon reference of the SGCN-96 is out of range for its partition")
	}
	sv, _ := c.take(41)
	numeral := sv.String()
	if numeral[0] != '1' || len(numeral) < 2 {
		return EPC{}, 0, epcErr("the SGCN-96 serial field %s is not a numeric string (a leading 1 and at least one digit)", numeral)
	}
	serial := numeral[1:]
	gcn := cp + ref + strconv.Itoa(gs1Check(cp+ref))
	return EPC{Filter: filter, Partition: part, CompanyPrefix: cp, Key: gcn, Serial: serial,
		URN:           fmt.Sprintf("urn:epc:id:sgcn:%s.%s.%s", cp, ref, serial),
		ElementString: "(255)" + gcn + serial}, c.pos, nil
}

// decodeITIP follows tables 14-43 and 14-44: the GTIN's partition table (14.4.3), piece and total as 2-digit fixed
// width integers (14.4.10), then a 38-bit numeric serial (ITIP-110) or up to 20 7-bit characters (ITIP-212).
func decodeITIP(b epcBits, long bool) (EPC, int, error) {
	scheme := "ITIP-110"
	if long {
		scheme = "ITIP-212"
	}
	c := &epcCursor{b: b}
	filter, part, p, cp, err := c.partitionHead(scheme, itipPartitions)
	if err != nil {
		return EPC{}, 0, err
	}
	rv, _ := c.take(p.refBits)
	ref, ok := zeroPad(rv, p.refDigits)
	if !ok {
		return EPC{}, 0, epcErr("the company prefix or reference of the %s is out of range for its partition", scheme)
	}
	pieces := [2]string{}
	for i, what := range []string{"piece", "total"} {
		v, _ := c.take(7)
		// 14.4.10: D = int(7 log 2 / log 10) = 2 digits, so at most 99.
		if v.Int64() > 99 {
			return EPC{}, 0, epcErr("the %s %s of the %s does not fit 2 digits", what, v, scheme)
		}
		pieces[i] = fmt.Sprintf("%02d", v.Int64())
	}
	var serial string
	if long {
		serial, err = decodeEPCString(b, c.pos, 140, 20)
		if err != nil {
			return EPC{}, 0, err
		}
		c.pos += 140
	} else {
		sv, _ := c.take(38)
		serial = sv.String()
	}
	gtin := ref[:1] + cp + ref[1:]
	gtin += strconv.Itoa(gs1Check(gtin))
	return EPC{Filter: filter, Partition: part, CompanyPrefix: cp, Key: gtin + pieces[0] + pieces[1], Serial: serial,
		URN:           fmt.Sprintf("urn:epc:id:itip:%s.%s.%s.%s.%s", cp, ref, pieces[0], pieces[1], urnEscape(serial)),
		ElementString: fmt.Sprintf("(8006)%s%s%s(21)%s", gtin, pieces[0], pieces[1], serial)}, c.pos, nil
}

// decodeADIVar follows table 14-30: a 6-bit filter, the CAGE code or DoDAAC (14.4.7), then the part number (0 to 32
// characters) and the serial (1 to 30), each in 6-bit characters ended by 000000 (14.4.8). "#" may only be the first
// character of the serial. The ADI is not a GS1 key: there is no element string, the company prefix is the CAGE code
// or DoDAAC, and the key is it followed by the part number.
func decodeADIVar(b epcBits) (EPC, int, error) {
	c := &epcCursor{b: b, pos: 8}
	f, err := c.take(6)
	if err != nil {
		return EPC{}, 0, err
	}
	var cage strings.Builder
	for i := 0; i < 6; i++ {
		v, err := c.take(6)
		if err != nil {
			return EPC{}, 0, err
		}
		if i == 0 && v.Int64() == 0x20 { // 100000 before a 5-character CAGE code
			continue
		}
		ch := sixBitChar(int(v.Int64()))
		if !cageChar(ch) {
			return EPC{}, 0, epcErr("the CAGE code or DoDAAC holds 6-bit value %06b, not a digit or a capital letter other than I and O", v.Int64())
		}
		cage.WriteByte(ch)
	}
	partNo, err := c.sixBitString("part number", 0, adiPartMax, adiChar)
	if err != nil {
		return EPC{}, 0, err
	}
	serial, err := c.sixBitString("serial", 1, adiSerialMax, func(ch byte) bool { return adiChar(ch) || ch == '#' })
	if err != nil {
		return EPC{}, 0, err
	}
	if strings.LastIndexByte(serial, '#') > 0 || serial == "#" {
		return EPC{}, 0, epcErr("\"#\" may only be the first character of an ADI serial, followed by at least one more")
	}
	d := cage.String()
	return EPC{Filter: int(f.Int64()), CompanyPrefix: d, Key: d + partNo, Serial: serial,
		URN: fmt.Sprintf("urn:epc:id:adi:%s.%s.%s", d, sixBitEscape(partNo), sixBitEscape(serial))}, c.pos, nil
}

// sixBitString reads 6-bit characters (TDS annex G) up to the 000000 terminator: at least min and at most max of
// them, each one allowed.
func (c *epcCursor) sixBitString(what string, minChars, maxChars int, allowed func(byte) bool) (string, error) {
	var sb strings.Builder
	for {
		v, err := c.take(6)
		if err != nil {
			return "", epcErr("the %s has no 000000 terminator before the tag ends", what)
		}
		if v.Sign() == 0 {
			break
		}
		if sb.Len() == maxChars {
			return "", epcErr("the %s is longer than %d characters", what, maxChars)
		}
		ch := sixBitChar(int(v.Int64()))
		if ch == 0 || !allowed(ch) {
			return "", epcErr("6-bit value %06b is not a character the %s can hold", v.Int64(), what)
		}
		sb.WriteByte(ch)
	}
	if sb.Len() < minChars {
		return "", epcErr("the %s is empty", what)
	}
	return sb.String(), nil
}

// sixBitChar maps a 6-bit value to its character of TDS table G-1 ("#", "-", "/", digits and capital letters, each
// the low six bits of its ASCII code), or 0.
func sixBitChar(v int) byte {
	switch {
	case v >= 1 && v <= 26:
		return byte(0x40 | v)
	case v >= 0x30 && v <= 0x39, v == 0x23, v == 0x2D, v == 0x2F:
		return byte(v)
	}
	return 0
}

func cpiChar(ch byte) bool { return sixBitChar(int(ch&0x3F)) == ch && ch != 0 }

// adiChar is ADIChar of section 6.3.18: a capital letter, a digit, "-" or "/".
func adiChar(ch byte) bool { return cpiChar(ch) && ch != '#' }

// cageChar is CAGECodeOrDODAACChar of section 6.3.17: a digit or a capital letter other than I and O.
func cageChar(ch byte) bool {
	return ch >= '0' && ch <= '9' || ch >= 'A' && ch <= 'Z' && ch != 'I' && ch != 'O'
}

// sixBitEscape gives the URI form of table G-1: "#" and "/" are escaped.
func sixBitEscape(s string) string {
	return strings.NewReplacer("#", "%23", "/", "%2F").Replace(s)
}

// tagURI is the EPC tag URI (section 12): the pure identity body with the coding scheme and filter in front.
func tagURI(e EPC) string {
	body := e.URN[strings.IndexByte(e.URN[len("urn:epc:id:"):], ':')+len("urn:epc:id:")+1:]
	if e.Scheme == "GID-96" {
		return "urn:epc:tag:gid-96:" + body
	}
	return fmt.Sprintf("urn:epc:tag:%s:%d.%s", strings.ToLower(e.Scheme), e.Filter, body)
}

// EncodeEPCTagURI builds the hexadecimal of a CPI-96, CPI-var, SGCN-96, ITIP-110, ITIP-212 or ADI-var tag from its EPC
// tag URI (TDS 14.3), e.g. urn:epc:tag:cpi-96:3.9521141.98765.12345, padded with zero bits to a whole 16-bit word as a
// reader reports it. The other schemes are built with EncodeEPC96 and EncodeEPCString.
func EncodeEPCTagURI(uri string) (string, error) {
	rest, ok := strings.CutPrefix(uri, "urn:epc:tag:")
	if !ok {
		return "", epcErr("an EPC tag URI starts with urn:epc:tag:")
	}
	enc, body, ok := strings.Cut(rest, ":")
	if !ok {
		return "", epcErr("the tag URI names no coding scheme")
	}
	want := map[string]int{"cpi-96": 4, "cpi-var": 4, "sgcn-96": 4, "itip-110": 6, "itip-212": 6, "adi-var": 4}[enc]
	if want == 0 {
		return "", epcErr("coding scheme %q is not one EncodeEPCTagURI builds (cpi-96, cpi-var, sgcn-96, itip-110, itip-212, adi-var)", enc)
	}
	// The serial is the last field and takes the rest: an ITIP-212 serial may hold a dot (table A-1); in the other
	// schemes a dot there is a character the serial cannot hold.
	parts := strings.SplitN(body, ".", want)
	if len(parts) != want {
		return "", epcErr("a %s tag URI has %d dot-separated fields, this has %d", enc, want, len(parts))
	}
	maxFilter := int64(7)
	if enc == "adi-var" {
		maxFilter = 63
	}
	filter, err := numericComponent("filter", parts[0], big.NewInt(maxFilter+1))
	if err != nil {
		return "", err
	}
	w := &epcWriter{v: new(big.Int)}
	switch enc {
	case "cpi-96":
		w.push(0x3C, 8)
		w.push(filter.Int64(), 3)
		p, err := w.prefix(parts[1], cpi96Partitions)
		if err != nil {
			return "", err
		}
		ref, err := numericComponent("part reference", parts[2], new(big.Int).Lsh(big.NewInt(1), uint(p.refBits)))
		if err != nil {
			return "", err
		}
		if len(parts[2]) > p.refDigits {
			return "", epcErr("a CPI-96 part reference is at most %d digits for a %d-digit company prefix", p.refDigits, p.cpDigits)
		}
		serial, err := numericComponent("serial", parts[3], new(big.Int).Lsh(big.NewInt(1), 31))
		if err != nil {
			return "", err
		}
		w.pushBig(ref, p.refBits)
		w.pushBig(serial, 31)
	case "cpi-var":
		w.push(0x3D, 8)
		w.push(filter.Int64(), 3)
		p, err := w.prefix(parts[1], cpiVarPartitions)
		if err != nil {
			return "", err
		}
		ref, err := unescapeSixBit("part reference", parts[2], cpiChar)
		if err != nil {
			return "", err
		}
		if ref == "" || len(ref) > p.refDigits {
			return "", epcErr("a CPI-var part reference is 1 to %d characters for a %d-digit company prefix", p.refDigits, p.cpDigits)
		}
		serial, err := numericComponent("serial", parts[3], big.NewInt(cpiVarSerialMax+1))
		if err != nil {
			return "", err
		}
		w.sixBit(ref)
		w.pushBig(serial, 40)
	case "sgcn-96":
		w.push(0x3F, 8)
		w.push(filter.Int64(), 3)
		p, err := w.prefix(parts[1], sgcnPartitions)
		if err != nil {
			return "", err
		}
		if len(parts[2]) != p.refDigits || !allDigitsOrEmpty(parts[2]) {
			return "", epcErr("an SGCN coupon reference is %d digits for a %d-digit company prefix", p.refDigits, p.cpDigits)
		}
		if !allDigits(parts[3]) || len(parts[3]) > 12 {
			return "", epcErr("an SGCN serial is 1 to 12 digits")
		}
		ref, _ := new(big.Int).SetString("0"+parts[2], 10)
		serial, _ := new(big.Int).SetString("1"+parts[3], 10) // 14.3.6: a leading 1 keeps the leading zeros
		w.pushBig(ref, p.refBits)
		w.pushBig(serial, 41)
	case "itip-110", "itip-212":
		long := enc == "itip-212"
		w.push(map[bool]int64{false: 0x40, true: 0x41}[long], 8)
		w.push(filter.Int64(), 3)
		p, err := w.prefix(parts[1], itipPartitions)
		if err != nil {
			return "", err
		}
		if len(parts[2]) != p.refDigits || !allDigits(parts[2]) {
			return "", epcErr("an ITIP indicator and item reference are %d digits for a %d-digit company prefix", p.refDigits, p.cpDigits)
		}
		ref, _ := new(big.Int).SetString(parts[2], 10)
		w.pushBig(ref, p.refBits)
		for i, what := range []string{"piece", "total"} {
			s := parts[3+i]
			if len(s) != 2 || !allDigits(s) {
				return "", epcErr("the ITIP %s is 2 digits", what)
			}
			n, _ := strconv.Atoi(s)
			w.push(int64(n), 7)
		}
		if long {
			serial, err := unescapeGS3A3(parts[5])
			if err != nil {
				return "", err
			}
			if serial == "" || len(serial) > 20 {
				return "", epcErr("an ITIP-212 serial is 1 to 20 characters")
			}
			for i := 0; i < 20; i++ {
				c := int64(0)
				if i < len(serial) {
					c = int64(serial[i])
				}
				w.push(c, 7)
			}
		} else {
			serial, err := numericComponent("serial", parts[5], new(big.Int).Lsh(big.NewInt(1), 38))
			if err != nil {
				return "", err
			}
			w.pushBig(serial, 38)
		}
	case "adi-var":
		w.push(0x3B, 8)
		w.push(filter.Int64(), 6)
		cage := parts[1]
		if len(cage) < 5 || len(cage) > 6 {
			return "", epcErr("a CAGE code is 5 characters and a DoDAAC 6")
		}
		if len(cage) == 5 {
			w.push(0x20, 6)
		}
		for i := 0; i < len(cage); i++ {
			if !cageChar(cage[i]) {
				return "", epcErr("character %q cannot be in a CAGE code or DoDAAC (digits and capital letters other than I and O)", cage[i])
			}
			w.push(int64(cage[i]&0x3F), 6)
		}
		partNo, err := unescapeSixBit("part number", parts[2], adiChar)
		if err != nil {
			return "", err
		}
		if len(partNo) > adiPartMax {
			return "", epcErr("an ADI part number is at most %d characters", adiPartMax)
		}
		serial, err := unescapeSixBit("serial", parts[3], func(ch byte) bool { return adiChar(ch) || ch == '#' })
		if err != nil {
			return "", err
		}
		if serial == "" || len(serial) > adiSerialMax {
			return "", epcErr("an ADI serial is 1 to %d characters", adiSerialMax)
		}
		if strings.LastIndexByte(serial, '#') > 0 || serial == "#" {
			return "", epcErr("\"#\" may only be the first character of an ADI serial, followed by at least one more")
		}
		w.sixBit(partNo)
		w.sixBit(serial)
	}
	return w.hex(), nil
}

type epcWriter struct {
	v    *big.Int
	bits int
}

func (w *epcWriter) push(x int64, width int) { w.pushBig(big.NewInt(x), width) }

func (w *epcWriter) pushBig(x *big.Int, width int) {
	w.v.Lsh(w.v, uint(width))
	w.v.Or(w.v, x)
	w.bits += width
}

// prefix writes the partition and company prefix of a padded company prefix whose length picks the partition.
func (w *epcWriter) prefix(cp string, table []epcPartition) (epcPartition, error) {
	if !allDigits(cp) {
		return epcPartition{}, epcErr("the company prefix must be digits")
	}
	for i, p := range table {
		if p.cpDigits == len(cp) {
			v, _ := new(big.Int).SetString(cp, 10)
			w.push(int64(i), 3)
			w.pushBig(v, p.cpBits)
			return p, nil
		}
	}
	return epcPartition{}, epcErr("a company prefix of %d digits has no partition (6 to 12 digits)", len(cp))
}

// sixBit writes 6-bit characters and the 000000 terminator (14.3.8, 14.3.9).
func (w *epcWriter) sixBit(s string) {
	for i := 0; i < len(s); i++ {
		w.push(int64(s[i]&0x3F), 6)
	}
	w.push(0, 6)
}

func (w *epcWriter) hex() string {
	padded := (w.bits + 15) / 16 * 16
	v := new(big.Int).Lsh(w.v, uint(padded-w.bits))
	return fmt.Sprintf("%0*X", padded/4, v)
}

// numericComponent reads a NumericComponent of section 5 (a decimal without leading zeros) below limit.
func numericComponent(what, s string, limit *big.Int) (*big.Int, error) {
	if !allDigits(s) || len(s) > 1 && s[0] == '0' {
		return nil, epcErr("the %s %q must be a decimal number without leading zeros", what, s)
	}
	v, _ := new(big.Int).SetString(s, 10)
	if v.Cmp(limit) >= 0 {
		return nil, epcErr("the %s %s is too large for its field", what, s)
	}
	return v, nil
}

// unescapeSixBit reads a CPRefComponent or ADI component: characters of table G-1 in their URI form, with "#" and "/"
// written %23 and %2F.
func unescapeSixBit(what, s string, allowed func(byte) bool) (string, error) {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '%' {
			if i+2 >= len(s) {
				return "", epcErr("an incomplete escape in the %s", what)
			}
			switch strings.ToUpper(s[i+1 : i+3]) {
			case "23":
				ch = '#'
			case "2F":
				ch = '/'
			default:
				return "", epcErr("escape %%%s cannot be in the %s (only %%23 and %%2F)", s[i+1:i+3], what)
			}
			i += 2
		} else if ch == '#' || ch == '/' {
			return "", epcErr("%q must be escaped in a URI", ch)
		}
		if !allowed(ch) {
			return "", epcErr("character %q cannot be in the %s", ch, what)
		}
		sb.WriteByte(ch)
	}
	return sb.String(), nil
}

// unescapeGS3A3 reads a GS3A3Component of section 5: characters of table A-1, the seven a URN cannot hold escaped.
func unescapeGS3A3(s string) (string, error) {
	var sb strings.Builder
	for i := 0; i < len(s); i++ {
		ch := s[i]
		if ch == '%' {
			if i+2 >= len(s) {
				return "", epcErr("an incomplete escape in the serial")
			}
			v, err := strconv.ParseUint(s[i+1:i+3], 16, 8)
			if err != nil || !epcChar(byte(v)) {
				return "", epcErr("escape %%%s is not a character a serial can hold", s[i+1:i+3])
			}
			ch = byte(v)
			i += 2
		} else if strings.IndexByte(`"%&/<>?`, ch) >= 0 || !epcChar(ch) {
			return "", epcErr("character %q cannot be in the serial as it is", ch)
		}
		sb.WriteByte(ch)
	}
	return sb.String(), nil
}
