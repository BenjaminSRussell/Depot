import { describe, expect, it } from "vitest";
import { gstinValid, ibanChecksumValid, luhnValid, validTaxId } from "../src/domain/identifiers.js";
import { scanPii, type PiiCategory } from "../src/domain/redaction.js";

const categories = (text: string): PiiCategory[] => scanPii(text).map((f) => f.category);

describe("identifier checksums", () => {
  it("accepts a valid IBAN and rejects a bad check digit", () => {
    expect(ibanChecksumValid("DE89 3704 0044 0532 0130 00")).toBe(true);
    expect(ibanChecksumValid("GB82WEST12345698765432")).toBe(true);
    expect(ibanChecksumValid("DE89 3704 0044 0532 0130 01")).toBe(false);
  });

  it("accepts a Luhn-valid card and rejects a bad digit and a 12-digit number", () => {
    expect(luhnValid("4111 1111 1111 1111")).toBe(true);
    expect(luhnValid("4111111111111112")).toBe(false);
    expect(luhnValid("411111111111")).toBe(false);
  });

  it("checks the GSTIN check character", () => {
    expect(gstinValid("27AAPFU0939F1ZV")).toBe(true);
    expect(gstinValid("27AAPFU0939F1ZW")).toBe(false);
  });

  it("accepts EIN, VAT and RFC shapes and rejects words", () => {
    for (const ok of ["12-3456789", "DE123456789", "XAXX010101000"]) expect(validTaxId(ok)).toBe(true);
    for (const bad of ["pending", "ABCDE12", "number"]) expect(validTaxId(bad)).toBe(false);
  });
});

describe("financial identifiers in text", () => {
  it.each([
    ["IBAN", "pay to DE89 3704 0044 0532 0130 00 today"],
    ["CARD", "card 4111 1111 1111 1111 expires soon"],
    ["CARD", "card 4111111111111111"],
    ["SSN", "ssn 123-45-6789"],
    ["TAXID", "EIN 12-3456789"],
    ["TAXID", "VAT: DE123456789"],
    ["TAXID", "GSTIN 27AAPFU0939F1ZV"],
    ["TAXID", "RFC XAXX010101000"],
    ["BANKACCT", "account number 00123456789"],
    ["BANKACCT", '{"bankAccount":"00123456789"}'],
    ["PAY", '{"salary":"85,000.50"}'],
    ["PAY", "salary: 85000"],
    ["PAY", '{"bonus":1200}'],
    ["TAXID", '{"taxId":"12-3456789"}'],
    ["TAXID", '{"tax_id":"12-3456789"}'],
    ["TAXID", '{"taxNumber":"123456789"}'],
    ["TAXID", '{"vatNumber":"DE123456789"}'],
    ["TAXID", '{"vatId":"DE123456789"}'],
    ["SSN", '{"ssn":"123456789"}'],
    ["IBAN", "pay to de89 3704 0044 0532 0130 00 today"],
    ["BANKACCT", '{"accountNumber":"1234 5678 9012"}'],
    ["BANKACCT", "account number 1234-5678-9012"],
    ["PAY", '{"salary":{"amount":85000}}'],
    ["PAY", '{"salary":{"currency":"USD","amount":85000}}'],
    ["PAY", '{"salary":"USD 85000"}'],
    ["PAY", "salary: $85,000"],
  ] as const)("finds %s in %s", (category, text) => {
    expect(categories(text)).toContain(category);
  });

  it.each([
    "invoice INV-2026-0042 total 1,200.50 USD",
    "DE89 3704 0044 0532 0130 01",
    "number 4111111111111112 is not a card",
    "reference 411111111111 and 1234567890123456789012",
    "account 1234 and account name Acme",
    "EIN number pending",
    "uuid 0192a2b4-0000-7000-8000-00000000000a",
    '{"salaryGrade":"B2","wageType":"hourly"}',
  ])("leaves %s untouched", (text) => {
    expect(scanPii(text)).toEqual([]);
  });
});
