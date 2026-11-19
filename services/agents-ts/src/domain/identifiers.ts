const GSTIN_ALPHABET = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZ";
const GSTIN_PATTERN = /^\d{2}[A-Z]{5}\d{4}[A-Z][1-9A-Z]Z[0-9A-Z]$/;
const EIN_PATTERN = /^\d{9}$/;
const RFC_PATTERN = /^[A-ZN&]{3,4}\d{6}[A-Z0-9]{3}$/;

export const VAT_PATTERNS: Readonly<Record<string, RegExp>> = {
  AT: /^U\d{8}$/,
  BE: /^[01]\d{9}$/,
  BG: /^\d{9,10}$/,
  CY: /^\d{8}[A-Z]$/,
  CZ: /^\d{8,10}$/,
  DE: /^\d{9}$/,
  DK: /^\d{8}$/,
  EE: /^\d{9}$/,
  EL: /^\d{9}$/,
  ES: /^[A-Z0-9]\d{7}[A-Z0-9]$/,
  FI: /^\d{8}$/,
  FR: /^[A-Z0-9]{2}\d{9}$/,
  HR: /^\d{11}$/,
  HU: /^\d{8}$/,
  IE: /^\d[A-Z0-9+*]\d{5}[A-Z]{1,2}$/,
  IT: /^\d{11}$/,
  LT: /^(\d{9}|\d{12})$/,
  LU: /^\d{8}$/,
  LV: /^\d{11}$/,
  MT: /^\d{8}$/,
  NL: /^\d{9}B\d{2}$/,
  PL: /^\d{10}$/,
  PT: /^\d{9}$/,
  RO: /^\d{2,10}$/,
  SE: /^\d{12}$/,
  SI: /^\d{8}$/,
  SK: /^\d{10}$/,
};

const compact = (raw: string): string => raw.replace(/[ .-]/g, "").toUpperCase();

export function ibanChecksumValid(raw: string): boolean {
  const iban = compact(raw);
  if (!/^[A-Z]{2}\d{2}[A-Z0-9]{11,30}$/.test(iban)) return false;
  let remainder = 0;
  for (const c of iban.slice(4) + iban.slice(0, 4)) {
    const digits = String(parseInt(c, 36));
    for (const d of digits) remainder = (remainder * 10 + Number(d)) % 97;
  }
  return remainder === 1;
}

export function luhnValid(raw: string): boolean {
  const digits = raw.replace(/[ -]/g, "");
  if (!/^\d{13,19}$/.test(digits)) return false;
  let sum = 0;
  for (let i = 0; i < digits.length; i += 1) {
    let d = Number(digits[digits.length - 1 - i]);
    if (i % 2 === 1) {
      d *= 2;
      if (d > 9) d -= 9;
    }
    sum += d;
  }
  return sum % 10 === 0;
}

export function gstinValid(raw: string): boolean {
  const number = compact(raw);
  if (!GSTIN_PATTERN.test(number)) return false;
  let sum = 0;
  for (let i = 0; i < 14; i += 1) {
    const product = GSTIN_ALPHABET.indexOf(number[i] ?? "") * (1 + (i % 2));
    sum += Math.floor(product / 36) + (product % 36);
  }
  return GSTIN_ALPHABET[(36 - (sum % 36)) % 36] === number[14];
}

export function validTaxId(raw: string): boolean {
  const number = compact(raw);
  if (EIN_PATTERN.test(number) || gstinValid(number) || RFC_PATTERN.test(number)) return true;
  const country = number.slice(0, 2) === "GR" ? "EL" : number.slice(0, 2);
  return (VAT_PATTERNS[country]?.test(number.slice(2)) ?? false) || Object.values(VAT_PATTERNS).some((p) => p.test(number));
}
