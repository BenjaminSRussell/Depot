import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, type TestInfo } from "@playwright/test";
import { expiredRows, parseAxeAllowlist } from "../src/shell/axeRoutes";
import allowlistJson from "./axeAllowlist.json" with { type: "json" };

const WCAG_TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];
const allowlist = parseAxeAllowlist(allowlistJson);

export async function scanRoute(page: Page, testInfo: TestInfo, route: string): Promise<void> {
  const results = await new AxeBuilder({ page }).withTags(WCAG_TAGS).analyze();
  const today = new Date().toISOString().slice(0, 10);
  const viewport = testInfo.project.name === "demo-phone" ? "phone" : "desktop";
  const rows = allowlist.filter((row) => row.route === route && row.viewports.includes(viewport));
  const expired = expiredRows(rows, today);
  const live = rows.filter((row) => !expired.includes(row));
  const failing = results.violations.filter((violation) => !live.some((row) => row.rule === violation.id));
  const stale = live.filter((row) => !results.violations.some((violation) => violation.id === row.rule));
  if (results.violations.length > 0) {
    await testInfo.attach(`violations ${route}`, { body: JSON.stringify(results.violations, null, 2), contentType: "application/json" });
  }
  expect(
    expired.map((row) => `${row.rule} expired ${row.expires}`),
    `expired axe allowlist rows on ${route}`,
  ).toEqual([]);
  expect(
    stale.map((row) => `${row.rule} no longer fires on the ${viewport} viewport`),
    `stale axe allowlist rows on ${route}; remove the viewport from e2e/axeAllowlist.json`,
  ).toEqual([]);
  expect(
    failing.map((v) => `${v.id} (${v.impact}): ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`),
    `axe violations on ${route}`,
  ).toEqual([]);
}
