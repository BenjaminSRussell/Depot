import { expect, test } from "@playwright/test";
import { scanRoute } from "./axe";
import { ShellPage } from "./shell.page";

test.describe("axe: dynamic states", () => {
  test("command palette open", async ({ page }, testInfo) => {
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openPaletteWithShortcut();
    await expect(shell.paletteOptions.first()).toBeVisible();
    await scanRoute(page, testInfo, "palette");
  });

  test("command palette with no results", async ({ page }, testInfo) => {
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openPaletteWithShortcut();
    await shell.paletteInput.fill("zzzz");
    await expect(page.getByText("No pages match")).toBeVisible();
    await scanRoute(page, testInfo, "palette-empty");
  });

  test("mobile drawer open", async ({ page }, testInfo) => {
    await page.setViewportSize({ width: 390, height: 844 });
    const shell = new ShellPage(page);
    await shell.goto("/");
    await shell.openDrawer();
    // Let the drawer finish sliding in: axe reads colours mid-fade as low contrast.
    await page.waitForFunction(() => document.getAnimations().every((a) => a.playState !== "running"));
    await scanRoute(page, testInfo, "drawer");
  });
});
