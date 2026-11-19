import { expect, test } from "@playwright/test";
import { scanRoute } from "./axe";
import { ShellPage } from "./shell.page";

const PROMPT_NAME = "Ask or do anything...";
const PAGE_CONTROLS = ":is(a[href], button, input, select, textarea):not([class*=\"topbar\"] *)";

const AVAILABLE = {
  killSwitch: { halted: false, stale: false, tenant: { active: false, version: null, changedAt: null, byUser: null }, global: { active: false, version: null, changedAt: null } },
  budgetStatus: "OK",
  copilotEnabled: true,
};

test.beforeEach(async ({ page }) => {
  await page.route("**/api/v1/agents/availability", (route) => route.fulfill({ json: AVAILABLE }));
});

for (const colorScheme of ["light", "dark"] as const) {
  for (const [label, viewport] of [
    ["mobile", { width: 375, height: 812 }],
    ["desktop", { width: 1280, height: 800 }],
  ] as const) {
    test.describe(`assistant-led home, ${colorScheme}, ${label}`, () => {
      test.use({ colorScheme, viewport });

      test("has no axe violations on the home route", async ({ page }, testInfo) => {
        await new ShellPage(page).goto("/");
        await expect(page.getByRole("textbox", { name: PROMPT_NAME })).toBeVisible();
        await scanRoute(page, testInfo, "home-assistant");
      });
    });
  }
}

test.describe("assistant-led home", () => {
  test("leads with the prompt: first in the main landmark and the largest control", async ({ page }) => {
    const shell = new ShellPage(page);
    await shell.goto("/");
    const prompt = page.getByRole("textbox", { name: PROMPT_NAME });
    await expect(prompt).toBeVisible();
    const firstControl = await shell.main.evaluate((main, selector) => main.querySelector(selector)?.id, PAGE_CONTROLS);
    expect(firstControl).toBe(await prompt.evaluate((el) => el.id));
    const area = async (locator: ReturnType<typeof page.locator>) => {
      const box = await locator.boundingBox();
      return box === null ? 0 : box.width * box.height;
    };
    const promptArea = await area(prompt);
    for (const control of await shell.main.locator(PAGE_CONTROLS).all()) {
      if ((await control.evaluate((el) => el.id)) === "home-assistant") continue;
      expect(promptArea).toBeGreaterThan(await area(control));
    }
  });

  test("reaches a module in at most two clicks and keeps See all features", async ({ page }) => {
    const shell = new ShellPage(page);
    await shell.goto("/");
    const region = page.getByRole("region", { name: "Browse workspaces" });
    await expect(region).toBeVisible();
    await region.getByRole("list", { name: /Top tasks/ }).first().getByRole("link").first().click();
    await expect(page).not.toHaveURL(/\/$/);
    await shell.goto("/");
    await expect(region.getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
  });

  test("keeps the skip link working with the prompt focused", async ({ page }) => {
    const shell = new ShellPage(page);
    await shell.goto("/");
    await expect(page.getByRole("textbox", { name: PROMPT_NAME })).toBeFocused();
    while (!(await shell.skipLink.evaluate((el) => el === document.activeElement))) await page.keyboard.press("Shift+Tab");
    await page.keyboard.press("Enter");
    await expect(shell.main).toBeFocused();
    await expect(page).toHaveURL(/#main$/);
  });
});
