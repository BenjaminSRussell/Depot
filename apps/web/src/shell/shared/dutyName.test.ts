// @vitest-environment node
import authz from "../../../../../policies/authz/data.json";
import { dutyName } from "./dutyName";

describe("dutyName", () => {
  it.each([
    ["sales.order.approve", "Approve sales order"],
    ["receivables.write-off.approve", "Approve receivables write off"],
    ["party.write", "Write party"],
  ])("names %s as %s", (code, name) => {
    expect(dutyName(code)).toBe(name);
  });

  it("gives every duty in the authorisation catalogue a name with no code in it", () => {
    const unnamed = Object.keys(authz.duties).filter((code) => /[.]|-/.test(dutyName(code)) || dutyName(code).length === 0);
    expect(unnamed).toEqual([]);
  });
});
