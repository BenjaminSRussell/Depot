import { expect, test, type Page } from "@playwright/test";
import { scanRoute } from "./axe";
import { ShellPage } from "./shell.page";

const AVAILABLE = {
  killSwitch: { halted: false, stale: false, tenant: { active: false, version: null, changedAt: null, byUser: null }, global: { active: false, version: null, changedAt: null } },
  budgetStatus: "OK",
  copilotEnabled: true,
};

test.use({ storageState: { cookies: [], origins: [] } });

async function stubAvailability(page: Page, copilotEnabled = true) {
  await page.route("**/api/v1/agents/availability", (route) => route.fulfill({ json: { ...AVAILABLE, copilotEnabled } }));
}

const tour = (page: Page) => page.getByRole("dialog", { name: /./ });

test.describe("first-run onboarding", () => {
  test("a new user sees the tour, skip persists across a reload, and Take the tour reopens it", async ({ page }) => {
    await stubAvailability(page);
    await new ShellPage(page).goto("/");
    await expect(page.getByText("Step 1 of 5", { exact: true })).toBeVisible();
    await page.getByRole("button", { name: "Skip tour" }).click();
    await expect(tour(page)).toHaveCount(0);

    await page.reload();
    await expect(page.getByRole("textbox", { name: "Ask or do anything..." })).toBeVisible();
    await expect(tour(page)).toHaveCount(0);

    await page.getByRole("button", { name: "Help for this page" }).click();
    await page.getByRole("button", { name: "Take the tour" }).click();
    await expect(page.getByText("Step 1 of 5", { exact: true })).toBeVisible();
    await page.keyboard.press("Escape");
    await expect(page.getByRole("button", { name: "Help for this page" })).toBeFocused();
  });

  test("finishing the tour returns focus to the prompt", async ({ page }) => {
    await stubAvailability(page);
    await new ShellPage(page).goto("/");
    await expect(page.getByText("Step 1 of 5", { exact: true })).toBeVisible();
    for (let step = 0; step < 4; step += 1) await page.getByRole("button", { name: "Next" }).click();
    await page.getByRole("button", { name: "Finish" }).click();
    await expect(page.getByRole("textbox", { name: "Ask or do anything..." })).toBeFocused();
  });

  test("a user whose assistant is off gets the reduced tour", async ({ page }) => {
    await stubAvailability(page, false);
    await new ShellPage(page).goto("/");
    await expect(page.getByText(/^Step 1 of [23]$/)).toBeVisible();
    await expect(tour(page)).not.toContainText(/assistant/i);
  });
});

for (const colorScheme of ["light", "dark"] as const) {
  test.describe(`onboarding axe, ${colorScheme}`, () => {
    test.use({ colorScheme });

    test("every step has no axe violations", async ({ page }, testInfo) => {
      await stubAvailability(page);
      await new ShellPage(page).goto("/");
      await expect(page.getByText("Step 1 of 5", { exact: true })).toBeVisible();
      for (let step = 1; step <= 5; step += 1) {
        await expect(page.getByText(`Step ${step} of 5`, { exact: true })).toBeVisible();
        await scanRoute(page, testInfo, `onboarding-step-${step}`);
        if (step < 5) await page.getByRole("button", { name: "Next" }).click();
      }
    });
  });
}
