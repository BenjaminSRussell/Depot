import { test } from "@playwright/test";
import { scanRoute } from "./axe";
import routes from "./axeRoutes.generated.json" with { type: "json" };

test.describe("axe: WCAG 2.2 AA on every workspace route", () => {
  for (const route of routes) {
    test(`${route} has no axe violations`, async ({ page }, testInfo) => {
      await page.goto(`.${route}`);
      await page.getByRole("heading", { level: 1 }).first().waitFor();
      await page.getByText("Loading…").first().waitFor({ state: "detached", timeout: 5000 }).catch(() => undefined);
      await scanRoute(page, testInfo, route);
    });
  }
});
