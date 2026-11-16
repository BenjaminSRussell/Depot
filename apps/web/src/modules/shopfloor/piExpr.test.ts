import { describe, expect, it } from "vitest";
import vectors from "../../../../../services/shopfloor/internal/domain/testdata/expr_vectors.json";
import { Dec } from "./piDecimal";
import { ExprEvalError, exprProblem, parseExpr } from "./piExpr";

describe("shared expression vectors (the service's evaluator must give the same text)", () => {
  const vars = vectors.vars as Record<string, string>;
  it("evaluates every case to the same text as the service", () => {
    for (const c of vectors.cases) {
      let got: string;
      try {
        const r = parseExpr(c.expr).eval(vars);
        got = String(r.value);
      } catch {
        got = "error";
      }
      expect(got, c.expr).toBe(c.want);
    }
  });

  it("refuses every expression the service refuses", () => {
    for (const src of vectors.refuse) expect(() => parseExpr(src), JSON.stringify(src)).toThrow();
  });
});

describe("step expressions", () => {
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
    expect(() => parseExpr("a + b").eval({ a: "1" })).toThrowError(ExprEvalError);
    try {
      parseExpr("a + b").eval({ a: "1" });
    } catch (error) {
      expect((error as ExprEvalError).missing).toBe("b");
    }
    expect(() => parseExpr("1 / x").eval({ x: "0" })).toThrow(/division by zero/);
    expect(() => parseExpr("a + 1").eval({ a: "not a number" })).toThrowError(ExprEvalError);
  });

  it("reads names as own values only", () => {
    expect(() => parseExpr("constructor + 1").eval({})).toThrow(ExprEvalError);
    expect(() => parseExpr("__proto__ + 1").eval({})).toThrow(ExprEvalError);
    expect(() => parseExpr("constructor(1)")).toThrow(/not a function/);
  });

  it("limits round to 0 to 12 places", () => {
    expect(() => parseExpr("round(1, 13)")).toThrow(/0 to 12/);
    expect(() => parseExpr("round(1, -1)")).toThrow(/0 to 12/);
    expect(() => parseExpr("round(x, y)").eval({ x: "1.234", y: "400" })).toThrow(/0 to 12/);
    expect(() => parseExpr("round(x, y)").eval({ x: "1.234", y: "1.5" })).toThrow(/0 to 12/);
    expect(parseExpr("round(x, y)").eval({ x: "1.2345", y: "2" })).toEqual({ type: "number", value: "1.23" });
  });
});

describe("exact decimals", () => {
  it("prints the way the service prints", () => {
    for (const [text, want] of [["12.50", "12.5"], ["007", "7"], ["-0.0", "0"], ["5.", "5"], [".5", "0.5"], ["+3", "3"]] as const) expect(Dec.parse(text)?.toString(), text).toBe(want);
    for (const bad of ["", "1e3", "1,5", "--1", "abc", "1.2.3"]) expect(Dec.parse(bad), bad).toBeNull();
    expect(Dec.fromNumber(0.1)?.toString()).toBe("0.1");
    expect(Dec.fromNumber(72)?.toString()).toBe("72");
    expect(Dec.fromNumber(1e-7)?.toString()).toBe("0.0000001");
    expect(Dec.fromNumber(NaN)).toBeNull();
  });
});
