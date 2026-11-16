// Amounts the customer types and the amounts the portal sends are decimal strings in the currency's minor unit. Arithmetic is on
// whole minor units (BigInt), never on floating point numbers, so 0.1 + 0.2 is 0.30 and a yen amount has no decimals.

/** ISO 4217 exponents that differ from the default of 2 (the same table as the services' money package). */
const EXPONENTS: Record<string, number> = {
  BHD: 3, IQD: 3, JOD: 3, KWD: 3, LYD: 3, OMR: 3, TND: 3,
  BIF: 0, CLP: 0, DJF: 0, GNF: 0, ISK: 0, JPY: 0, KMF: 0, KRW: 0, PYG: 0, RWF: 0, UGX: 0, UYI: 0, VND: 0, VUV: 0, XAF: 0, XOF: 0, XPF: 0,
};

export function minorDigits(currency: string): number {
  return EXPONENTS[currency] ?? 2;
}

/**
 * The amount in minor units, or null when the text is not a plain amount with at most the currency's decimals. A minus sign is
 * kept (open credits are negative); the caller decides whether a negative amount is allowed.
 */
export function toMinor(text: string, currency: string): bigint | null {
  const t = text.trim().replace(/,/g, "");
  const digits = minorDigits(currency);
  const m = /^(-?)(\d+)(?:\.(\d*))?$/.exec(t);
  if (!m) return null;
  const frac = (m[3] ?? "").replace(/0+$/, "");
  if (frac.length > digits) return null;
  const value = BigInt(m[2] as string) * 10n ** BigInt(digits) + BigInt((frac || "0").padEnd(digits, "0").slice(0, digits) || "0");
  return m[1] ? -value : value;
}

/** Minor units back to a decimal string with the currency's decimals ("1250.50", "5000" for JPY). */
export function fromMinor(minor: bigint, currency: string): string {
  const digits = minorDigits(currency);
  const negative = minor < 0n;
  const abs = negative ? -minor : minor;
  const scale = 10n ** BigInt(digits);
  const whole = (abs / scale).toString();
  const frac = digits > 0 ? `.${(abs % scale).toString().padStart(digits, "0")}` : "";
  return `${negative ? "-" : ""}${whole}${frac}`;
}

/** An amount as the customer reads it: "1,250.50 USD". */
export function display(amount: string, currency: string): string {
  const minor = toMinor(amount, currency);
  if (minor === null) return `${amount} ${currency}`;
  const text = fromMinor(minor, currency);
  const [whole, frac] = text.replace("-", "").split(".");
  const grouped = (whole as string).replace(/\B(?=(\d{3})+(?!\d))/g, ",");
  return `${minor < 0n ? "-" : ""}${grouped}${frac !== undefined ? `.${frac}` : ""} ${currency}`;
}
