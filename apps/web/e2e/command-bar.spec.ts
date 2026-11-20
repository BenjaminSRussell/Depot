import { expect, test, type Page } from "@playwright/test";
import { scanRoute } from "./axe";
import { ShellPage } from "./shell.page";

const AVAILABLE = {
  killSwitch: { halted: false, stale: false, tenant: { active: false, version: null, changedAt: null, byUser: null }, global: { active: false, version: null, changedAt: null } },
  budgetStatus: "OK",
  copilotEnabled: true,
};
const VENDOR = { type: "party", id: "v-1", number: "V-0007", title: "Acme Tooling", subtitle: "organization", status: "active", link: "/party/v-1" };

async function stubApi(page: Page, availability: object = AVAILABLE) {
  await page.route("**/api/v1/agents/availability", (route) => route.fulfill({ json: availability }));
  await page.route("**/api/v1/search?**", (route) => route.fulfill({ json: { results: [VENDOR] } }));
  await page.route("**/api/v1/copilot/chat", (route) => route.fulfill({ json: { kind: "ask", confidence: 0.9 } }));
}

test.describe("command bar", () => {
  for (const path of ["/", "/features", "/glossary"]) {
    test(`opens with Ctrl+K on ${path} and returns focus on Escape`, async ({ page }) => {
      await stubApi(page);
      const shell = new ShellPage(page);
      await shell.goto(path);
      await shell.openPaletteWithShortcut();
      await expect(shell.paletteInput).toBeFocused();
      await page.keyboard.press("Escape");
      await expect(shell.palette).toHaveCount(0);
    });
  }

  test("opens from the top-bar Search or ask button", async ({ page }) => {
    await stubApi(page);
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.commandBarButton.click();
    await expect(shell.paletteInput).toBeFocused();
    await page.keyboard.press("Escape");
    await expect(shell.commandBarButton).toBeFocused();
  });

  test("looks up a vendor in Records and opens it", async ({ page }) => {
    await stubApi(page);
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openPaletteWithShortcut();
    await shell.paletteInput.fill("acme");
    await expect(shell.palette.getByRole("group", { name: "Records" }).getByRole("option", { name: /Party V-0007 Acme Tooling/ })).toBeVisible();
    await expect(shell.palette.getByRole("group", { name: "Assistant" })).toBeVisible();
    await shell.palette.getByRole("option", { name: /Party V-0007/ }).click();
    await expect(page).toHaveURL(/\/party\/v-1$/);
    await expect(shell.palette).toHaveCount(0);
  });

  test("jumps to a Navigate entry", async ({ page }) => {
    await stubApi(page);
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openPaletteWithShortcut();
    await shell.paletteInput.fill("glossary");
    await shell.palette.getByRole("group", { name: "Navigate" }).getByRole("option", { name: /^Glossary/ }).click();
    await expect(page).toHaveURL(/\/glossary$/);
  });

  test("sends text with no match to the assistant", async ({ page }) => {
    await stubApi(page);
    await page.route("**/api/v1/search?**", (route) => route.fulfill({ json: { results: [] } }));
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openPaletteWithShortcut();
    await shell.paletteInput.fill("zzqqxx");
    await expect(shell.palette.getByRole("option", { name: /Ask the assistant: zzqqxx/ })).toBeVisible();
    await page.keyboard.press("Enter");
    await expect(shell.palette).toHaveCount(0);
    await expect(page).toHaveURL(/\/copilot$/);
  });

  test("keeps pages and records working and explains the missing assistant when AI is down", async ({ page }) => {
    await stubApi(page, { ...AVAILABLE, copilotEnabled: false });
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openPaletteWithShortcut();
    await shell.paletteInput.fill("acme");
    await expect(shell.palette.getByRole("option", { name: /Party V-0007/ })).toBeVisible();
    await expect(shell.palette.getByText(/The assistant is turned off for your company/)).toBeVisible();
    await expect(shell.palette.getByRole("group", { name: "Assistant" })).toHaveCount(0);
  });

  for (const [label, viewport] of [
    ["mobile", { width: 375, height: 812 }],
    ["desktop", { width: 1280, height: 800 }],
  ] as const) {
    test.describe(`axe, ${label}`, () => {
      test.use({ viewport });

      test("has no violations with the dialog open and traps focus", async ({ page }, testInfo) => {
        await stubApi(page);
        const shell = new ShellPage(page);
        await shell.goto("/");
        await shell.openPaletteWithShortcut();
        await shell.paletteInput.fill("acme");
        await expect(shell.palette.getByRole("option", { name: /Party V-0007/ })).toBeVisible();
        await scanRoute(page, testInfo, "command-bar");
        for (let i = 0; i < 4; i++) {
          await page.keyboard.press("Tab");
          expect(await shell.palette.evaluate((dialog) => dialog.contains(document.activeElement))).toBe(true);
        }
      });
    });
  }
});
