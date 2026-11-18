// @vitest-environment node
import { ID_PROMPT, PROBLEM_CODE, RAW_ENUM, UUID_WORD, dutyCode } from "./plainLanguageRules";

const DUTIES = ["sales.order.approve", "audit.read"];

const fixtures: Record<string, { rule: RegExp; flagged: string[]; fine: string[] }> = {
  UUID_WORD: {
    rule: UUID_WORD,
    flagged: ["Enter a UUID.", "Paste the order uuid here", "Customer UUIDs are listed below"],
    fine: ["Enter the order number.", "Duration of the job"],
  },
  ID_PROMPT: {
    rule: ID_PROMPT,
    flagged: ["Enter the order id", "Type a vendor identifier", "Paste the legal entity ID"],
    fine: ["Enter the order number", "Your id card is on file", "Identify the vendor"],
  },
  DUTY_CODE: {
    rule: dutyCode(DUTIES),
    flagged: ["Needs the sales.order.approve duty", "Ask for audit.read."],
    fine: ["Needs approval rights for sales orders", "Open sales.order.approver.png", "my.audit.read"],
  },
  PROBLEM_CODE: {
    rule: PROBLEM_CODE,
    flagged: ["Could not load (REPORT-0010)", "FIN-123 failed"],
    fine: ["Could not load the report", "Order SO-1 shipped", "UTF-8 text"],
  },
  RAW_ENUM: {
    rule: RAW_ENUM,
    flagged: ["Status: PENDING_APPROVAL", "Moved to IN_TRANSIT_2"],
    fine: ["Pending approval", "Status: PENDING", "snake_case_name"],
  },
};

describe("plain-language rule fixtures", () => {
  for (const [name, { rule, flagged, fine }] of Object.entries(fixtures)) {
    it(`${name} flags what people should not read and leaves plain text alone`, () => {
      expect(flagged.filter((text) => !rule.test(text))).toEqual([]);
      expect(fine.filter((text) => rule.test(text))).toEqual([]);
    });
  }

  it("builds the duty rule from the duty names, escaping their dots", () => {
    expect(dutyCode(["audit.read"]).test("auditXread")).toBe(false);
  });
});
