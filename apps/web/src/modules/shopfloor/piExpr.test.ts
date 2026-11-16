import { describe, expect, it } from "vitest";
import { ExprEvalError, exprProblem, parseExpr } from "./piExpr";

const vars = { gross: 12.5, tare: 0.3, n: 4, zero: 0 };
const val = (src: string) => {
  const r = parseExpr(src).eval(vars);
  return r.value;
};

describe("step expressions", () => {
  it("evaluates what the service evaluates", () => {
    for (const [src, want] of Object.entries({
      "gross - tare": 12.2,
      "0.1 + 0.2": 0.3,
      "(gross - tare) / n": 3.05,
      "-gross + 20": 7.5,
      "2 + 3 * 4": 14,
      "(2 + 3) * 4": 20,
      "10 / 4": 2.5,
      "abs(tare - gross)": 12.2,
      "min(gross, tare, n)": 0.3,
      "max(gross, n)": 12.5,
      "round(gross / 3, 2)": 4.17,
      "floor(gross)": 12,
      "ceil(gross)": 13,
      "gross > 10 && n == 4": true,
      "gross > 10 and n != 4": false,
      "not (gross < 10) or zero > 1": true,
      "zero != 0 && 10 / zero > 2": false,
      "gross <= 12.5 || 1 / zero > 1": true,
      "gross >= 12.51": false,
      "!(gross == 12.5)": false,
    })) {
      expect(val(src), src).toBe(want);
    }
  });

  it("knows its type and the names it reads", () => {
    const e = parseExpr("b * 2 + a - b");
    expect(e.type).toBe("number");
    expect(e.names).toEqual(["a", "b"]);
    expect(parseExpr("a > 1 and b < 2").type).toBe("boolean");
    expect(exprProblem("a + 1", "number")).toBeNull();
    expect(exprProblem("a > 1", "number")).toMatch(/must compute a number/);
    expect(exprProblem("a + 1", "boolean")).toMatch(/true or false/);
  });

  it("reports a missing value and a division by zero", () => {
    expect(() => parseExpr("a + b").eval({ a: 1 })).toThrowError(ExprEvalError);
    try {
      parseExpr("a + b").eval({ a: 1 });
    } catch (error) {
      expect((error as ExprEvalError).missing).toBe("b");
    }
    expect(() => parseExpr("1 / x").eval({ x: 0 })).toThrow(/division by zero/);
  });

  it("refuses everything that is not the grammar", () => {
    for (const src of [
      "", "sqrt(4)", "system(1)", "1; 2", "a = 1", '"x"', "a.b", "a[0]", "Gross + 1", "(1 + 2", "1 + 2)", "1 +", "1 + * 2", "1 2", "1.2.3", "2x",
      "(a > 1) + 2", "a and b", "not a", "(a > 1) == (b > 1)", "1 < a < 3", "round(1)", "abs(1, 2)", "min()", "abs(a > 1)", "${a}", "`a`", "a | b", "a & b",
      "(".repeat(40) + "1" + ")".repeat(40), "1" + " + 1".repeat(120), "1".repeat(501),
    ]) {
      expect(() => parseExpr(src), JSON.stringify(src).slice(0, 40)).toThrow();
    }
    // Nothing reaches the global scope: a constructor name is only a name with no value.
    expect(() => parseExpr("constructor + 1").eval({})).toThrow(ExprEvalError);
    expect(() => parseExpr("__proto__ + 1").eval({})).toThrow(ExprEvalError);
    expect(() => parseExpr("constructor(1)")).toThrow(/not a function/);
  });
});
