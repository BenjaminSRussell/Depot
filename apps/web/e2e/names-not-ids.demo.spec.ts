import { expect, test } from "@playwright/test";
import { ShellPage } from "./shell.page";

const UUID = /[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/i;

const ENTRY_ROUTES = [
  "/sales/orders/new",
  "/sales/order-progress",
  "/purchasing/purchase-orders",
  "/planning/planned-orders",
  "/planning/supply-schedule",
];

test.describe("names, not ids", () => {
  for (const route of ENTRY_ROUTES) {
    test(`${route} shows no raw id`, async ({ page }) => {
      const shell = new ShellPage(page);
      await shell.goto(`.${route}`);
      await expect(shell.main.getByRole("heading").first()).toBeVisible();
      await expect(shell.main).not.toContainText(UUID);
      const typed = await shell.main.locator("input:not([type=hidden]):not([type=checkbox])").evaluateAll((inputs) => inputs.map((input) => (input as HTMLInputElement).value));
      expect(typed.filter((value) => UUID.test(value))).toEqual([]);
    });
  }

  test("the planner scopes a run by pasting item numbers", async ({ page }) => {
    const shell = new ShellPage(page);
    await shell.goto("./planning/runs/new");
    const items = page.getByLabel("Items in scope");
    await items.evaluate((field) => {
      const data = new DataTransfer();
      data.setData("text", "CP-200, SEAL-MK-125\nNOPE-1");
      field.dispatchEvent(new ClipboardEvent("paste", { clipboardData: data, bubbles: true, cancelable: true }));
    });
    await expect(shell.main.getByText("Centrifugal pump, 2 in, 5 HP (CP-200)")).toBeVisible();
    await expect(shell.main.getByText("Mechanical seal kit, 1.25 in (SEAL-MK-125)")).toBeVisible();
    await expect(shell.main.getByText("Not found: NOPE-1")).toBeVisible();
    await expect(page.getByRole("group", { name: "Plan scope" })).not.toContainText(UUID);
    await shell.main.getByRole("button", { name: "Remove Centrifugal pump, 2 in, 5 HP (CP-200)" }).click();
    await expect(shell.main.getByText("Centrifugal pump, 2 in, 5 HP (CP-200)")).toHaveCount(0);
  });
});
