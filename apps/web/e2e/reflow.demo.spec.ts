import { expect, test } from "@playwright/test";
import routes from "./axeRoutes.generated.json" with { type: "json" };

for (const width of [390, 320]) {
  test.describe(`reflow at ${width}px`, () => {
    test.use({ viewport: { width, height: 844 } });

    for (const route of routes) {
      test(`${route} does not scroll sideways`, async ({ page }) => {
        await page.goto(`.${route}`);
        await page.getByRole("heading", { level: 1 }).first().waitFor();
        await page.getByText("Loading…").first().waitFor({ state: "detached", timeout: 5000 }).catch(() => undefined);
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
        expect(overflow, `horizontal overflow on ${route}`).toBeLessThanOrEqual(0);
      });
    }

    test("the kiosk top bar keeps the work context in a sheet", async ({ page }) => {
      await page.goto("./shopfloor/kiosk");
      const opener = page.getByRole("button", { name: "Context", exact: true });
      await opener.click();
      const sheet = page.getByRole("dialog", { name: "Context and account" });
      await expect(sheet).toBeVisible();
      await expect(sheet.getByRole("button", { name: "Help for this page" })).toBeVisible();
      await page.keyboard.press("Escape");
      await expect(sheet).toHaveCount(0);
      await expect(opener).toBeFocused();
    });
  });
}
