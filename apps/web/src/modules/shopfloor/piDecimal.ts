// Exact decimal arithmetic for the step expressions of the PI step library, so the kiosk gets the same numbers as the service
// (shopspring/decimal in services/shopfloor/internal/domain/expr.go): addition, subtraction and multiplication are exact,
// division is carried to 12 places and round() goes to 0 to 12 places, both rounding half away from zero. A value is
// n / 10^s with a BigInt n; trailing zeros are dropped when it is printed, as the service prints them.

export const DIVISION_PLACES = 12;
export const MAX_ROUND_PLACES = 12;

const TEN = 10n;
const pow10 = (n: number): bigint => TEN ** BigInt(n);

export class Dec {
  private constructor(
    readonly n: bigint,
    readonly s: number,
  ) {}

  static of(n: bigint, s = 0): Dec {
    // Normalise: no trailing zeros in the fraction.
    while (s > 0 && n % TEN === 0n) {
      n /= TEN;
      s--;
    }
    return new Dec(n, s);
  }

  /** Parses decimal text such as "12.50", "-.5" or "7"; null for anything else (no exponent, no separators). */
  static parse(text: string): Dec | null {
    const m = /^([+-]?)(\d+)(?:\.(\d*))?$|^([+-]?)\.(\d+)$/.exec(text.trim());
    if (!m) return null;
    const sign = (m[1] ?? m[4]) === "-" ? -1n : 1n;
    const whole = m[2] ?? "0";
    const frac = m[3] ?? m[5] ?? "";
    return Dec.of(sign * BigInt(whole + frac), frac.length);
  }

  /** From a JavaScript number the way the service reads the same number (its shortest decimal form). */
  static fromNumber(x: number): Dec | null {
    if (!Number.isFinite(x)) return null;
    const text = String(x);
    if (!/[eE]/.test(text)) return Dec.parse(text);
    return Dec.parse(x.toFixed(20).replace(/0+$/, "").replace(/\.$/, ""));
  }

  private align(o: Dec): [bigint, bigint, number] {
    const s = Math.max(this.s, o.s);
    return [this.n * pow10(s - this.s), o.n * pow10(s - o.s), s];
  }

  add(o: Dec): Dec {
    const [a, b, s] = this.align(o);
    return Dec.of(a + b, s);
  }
  sub(o: Dec): Dec {
    const [a, b, s] = this.align(o);
    return Dec.of(a - b, s);
  }
  mul(o: Dec): Dec {
    return Dec.of(this.n * o.n, this.s + o.s);
  }
  neg(): Dec {
    return Dec.of(-this.n, this.s);
  }
  abs(): Dec {
    return this.n < 0n ? this.neg() : this;
  }
  cmp(o: Dec): -1 | 0 | 1 {
    const [a, b] = this.align(o);
    return a < b ? -1 : a > b ? 1 : 0;
  }
  isZero(): boolean {
    return this.n === 0n;
  }
  isInteger(): boolean {
    return this.s === 0;
  }

  /** this / o to DIVISION_PLACES places, half away from zero. o must not be zero. */
  div(o: Dec): Dec {
    const num = this.n * pow10(o.s + DIVISION_PLACES);
    const den = o.n * pow10(this.s);
    return Dec.of(roundDiv(num, den), DIVISION_PLACES);
  }

  /** Rounds to the given number of places (0 or more), half away from zero. */
  round(places: number): Dec {
    if (this.s <= places) return this;
    const drop = pow10(this.s - places);
    return Dec.of(roundDiv(this.n, drop), places);
  }
  floor(): Dec {
    if (this.s === 0) return this;
    const unit = pow10(this.s);
    let q = this.n / unit;
    if (this.n < 0n && this.n % unit !== 0n) q -= 1n;
    return Dec.of(q, 0);
  }
  ceil(): Dec {
    if (this.s === 0) return this;
    const unit = pow10(this.s);
    let q = this.n / unit;
    if (this.n > 0n && this.n % unit !== 0n) q += 1n;
    return Dec.of(q, 0);
  }

  toString(): string {
    const neg = this.n < 0n;
    const digits = (neg ? -this.n : this.n).toString().padStart(this.s + 1, "0");
    const whole = digits.slice(0, digits.length - this.s);
    const frac = digits.slice(digits.length - this.s);
    return `${neg ? "-" : ""}${whole}${this.s > 0 ? `.${frac}` : ""}`;
  }

  toNumber(): number {
    return Number(this.toString());
  }
}

/** num / den rounded half away from zero (den is never zero). */
function roundDiv(num: bigint, den: bigint): bigint {
  const neg = num < 0n !== den < 0n;
  const a = num < 0n ? -num : num;
  const b = den < 0n ? -den : den;
  let q = a / b;
  if ((a % b) * 2n >= b) q += 1n;
  return neg ? -q : q;
}
