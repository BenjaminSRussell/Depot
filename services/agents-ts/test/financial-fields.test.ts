import { describe, expect, it } from "vitest";
import { applyFinancialPolicy, fieldClassOf, type FinancialFieldPolicy } from "../src/domain/financialFields.js";
import { Pseudonymizer } from "../src/domain/redaction.js";

const pseudo = (): Pseudonymizer => new Pseudonymizer("t", 60_000, () => 0);

describe("financial field policy", () => {
  it("keeps allowed amounts exactly", () => {
    const out = applyFinancialPolicy({ id: "INV-1", total: "1200.00", openAmount: 5, creditLimit: 9000 }, pseudo());
    expect(out).toEqual({ value: { id: "INV-1", total: "1200.00", openAmount: 5, creditLimit: 9000 }, withheld: 0 });
  });

  it("removes compensation fields in any key spelling and counts them", () => {
    const out = applyFinancialPolicy({ rows: [{ pay_rate: 45.5, base_pay: 5000, name: "x" }, { salary: { amount: 1 } }] }, pseudo());
    expect(out).toEqual({ value: { rows: [{ name: "x" }, {}] }, withheld: 3 });
  });

  it("never allows compensation even when the policy says allow", () => {
    const policy: FinancialFieldPolicy = { DOCUMENT_AMOUNT: "allow", BALANCE: "allow", CREDIT_LIMIT: "allow", BANK_BALANCE: "allow", COMPENSATION: "allow" };
    expect(applyFinancialPolicy({ salary: 1 }, pseudo(), policy)).toEqual({ value: {}, withheld: 1 });
  });

  it("tokenizes bank balances with one stable pseudonym per value", () => {
    const p = pseudo();
    const out = applyFinancialPolicy({ bankBalance: 100, cashPosition: 100, other: { accountBalance: 7 } }, p);
    expect(out.value).toEqual({ bankBalance: "[AMOUNT-1]", cashPosition: "[AMOUNT-1]", other: { accountBalance: "[AMOUNT-2]" } });
    expect(p.restore("[AMOUNT-2]")).toBe("7");
  });

  it("withholds a registered class that has no policy entry and a tokenized object", () => {
    const policy = { DOCUMENT_AMOUNT: "tokenize" } as unknown as FinancialFieldPolicy;
    expect(applyFinancialPolicy({ total: { a: 1 }, balance: 2 }, pseudo(), policy)).toEqual({ value: {}, withheld: 2 });
  });

  it("does not class unregistered keys", () => {
    expect(fieldClassOf("salaryGrade")).toBeUndefined();
    expect(fieldClassOf("Credit_Limit")).toBe("CREDIT_LIMIT");
  });
});
