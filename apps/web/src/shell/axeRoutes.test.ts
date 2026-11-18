// @vitest-environment node
import { loadModules } from "../modules/registry";
import { axeRoutes, expiredRows, parseAxeAllowlist, type AxeAllowlistRow } from "./axeRoutes";
import navAudit from "./workspaces/navAudit.json";
import { shellNav } from "./shellNav";

const row: AxeAllowlistRow = { route: "/sales/orders", rule: "color-contrast", viewports: ["desktop"], reason: "Tracked in the design system backlog", expires: "2027-03-31" };

describe("routes scanned by axe", () => {
  it("is the committed list, built from the shell nav and every module nav", async () => {
    const routes = axeRoutes(loadModules(), shellNav);
    await expect(`${JSON.stringify(routes, null, 2)}\n`).toMatchFileSnapshot("../../e2e/axeRoutes.generated.json");
  });

  it("covers the design system, the not-found page and every workspace entry", () => {
    const routes = axeRoutes(loadModules(), shellNav);
    expect(routes).toContain("/design-system");
    expect(routes).toContain("/no-such-page");
    expect(navAudit.entries.map((entry) => entry.to).filter((to) => !routes.includes(to))).toEqual([]);
  });
});

describe("axe allowlist", () => {
  it("accepts a complete row", () => {
    expect(parseAxeAllowlist([row])).toEqual([row]);
    expect(parseAxeAllowlist([])).toEqual([]);
  });

  it.each([
    ["a row without a date", { ...row, expires: undefined }],
    ["a number as the date", { ...row, expires: 20270331 }],
    ["a date that does not exist", { ...row, expires: "2027-02-31" }],
    ["a date in another format", { ...row, expires: "31/03/2027" }],
    ["a row without a rule", { ...row, rule: "" }],
    ["a row without a reason", { ...row, reason: "" }],
    ["an empty route", { ...row, route: "" }],
    ["no viewport", { ...row, viewports: [] }],
    ["an unknown viewport", { ...row, viewports: ["tablet"] }],
    ["an unknown field", { ...row, note: "x" }],
  ])("fails closed on %s", (_name, bad) => {
    expect(() => parseAxeAllowlist([bad])).toThrow();
  });

  it("fails closed when the file is not a list", () => {
    expect(() => parseAxeAllowlist({})).toThrow();
  });

  it("reports a row whose date has passed, and keeps one that expires today", () => {
    expect(expiredRows([row], "2027-04-01")).toEqual([row]);
    expect(expiredRows([row], "2027-03-31")).toEqual([]);
  });
});
