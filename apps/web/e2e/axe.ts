import AxeBuilder from "@axe-core/playwright";
import { expect, type Page, type TestInfo } from "@playwright/test";
import { expiredRows, parseAxeAllowlist } from "../src/shell/axeRoutes";
import allowlistJson from "./axeAllowlist.json" with { type: "json" };

const WCAG_TAGS = ["wcag2a", "wcag2aa", "wcag21a", "wcag21aa", "wcag22aa"];
const allowlist = parseAxeAllowlist(allowlistJson);

export async function scanRoute(page: Page, testInfo: TestInfo, route: string): Promise<void> {
  const results = await new AxeBuilder({ page }).withTags(WCAG_TAGS).analyze();
  const today = new Date().toISOString().slice(0, 10);
  const rows = allowlist.filter((row) => row.route === route);
  const expired = expiredRows(rows, today);
  const live = rows.filter((row) => !expired.includes(row));
  const failing = results.violations.filter((violation) => !live.some((row) => row.rule === violation.id));
  for (const row of live.filter((row) => !results.violations.some((violation) => violation.id === row.rule))) {
    testInfo.annotations.push({ type: "stale-allowlist-row", description: `${row.rule} on ${route} no longer fires; remove it from e2e/axeAllowlist.json` });
  }
  if (results.violations.length > 0) {
    await testInfo.attach(`violations ${route}`, { body: JSON.stringify(results.violations, null, 2), contentType: "application/json" });
  }
  expect(
    expired.map((row) => `${row.rule} expired ${row.expires}`),
    `expired axe allowlist rows on ${route}`,
  ).toEqual([]);
  expect(
    failing.map((v) => `${v.id} (${v.impact}): ${v.nodes.map((n) => n.target.join(" ")).join(", ")}`),
    `axe violations on ${route}`,
  ).toEqual([]);
}
