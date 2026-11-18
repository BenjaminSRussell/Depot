import { expect, test, type Locator, type Page } from "@playwright/test";
import budgets from "./taskBudgets.json" with { type: "json" };
import { ShellPage } from "./shell.page";

interface Budget {
  budget: number | null;
  round0: number | null;
  script: string | null;
  reason?: string;
}

class Interactions {
  count = 0;

  constructor(private readonly page: Page) {}

  async click(target: Locator): Promise<void> {
    this.count += 1;
    await target.click();
  }

  async fill(target: Locator, value: string): Promise<void> {
    this.count += 1;
    await target.fill(value);
  }

  async press(key: string): Promise<void> {
    this.count += 1;
    await this.page.keyboard.press(key);
  }
}

const SCRIPTS: Record<string, { start: string; run: (page: Page, act: Interactions) => Promise<void> }> = {
  "on-hand-by-product": {
    start: "./inventory/onhand",
    run: async (page, act) => {
      await act.fill(page.getByRole("combobox", { name: "Product" }), "CP-200");
      await act.click(page.getByRole("option", { name: /CP-200/ }).first());
      await act.click(page.getByRole("button", { name: "Apply filters" }));
      await expect(page.getByRole("row", { name: /Centrifugal pump, 2 in, 5 HP \(CP-200\)/ }).first()).toBeVisible();
    },
  },
  "trial-balance": {
    start: "./ledger/trial-balance",
    run: async (page, act) => {
      await act.click(page.getByRole("combobox", { name: "Ledger" }));
      await act.click(page.getByRole("listbox", { name: "ledger matches" }).getByRole("option").first());
      await act.click(page.getByRole("button", { name: "Show trial balance" }));
      await expect(page.getByRole("table")).toBeVisible();
    },
  },
};

const entries = Object.entries(budgets as Record<string, Budget>);

test.describe("task budgets: scripted paths stay within budget or their recorded Round 0 count", () => {
  for (const [id, entry] of entries) {
    test(`${id} ${entry.script ? `runs ${entry.script}` : "has no scripted path"}`, async ({ page }) => {
      test.skip(entry.script === null, `not scripted: ${entry.reason ?? "no reason recorded"}`);
      const script = SCRIPTS[entry.script as string];
      expect(script, `${id}: no script named ${entry.script}`).toBeDefined();

      await new ShellPage(page).goto(script.start);
      const act = new Interactions(page);
      await script.run(page, act);

      const ceiling = Math.max(entry.budget ?? 0, entry.round0 ?? 0);
      expect(act.count, `${id} took ${act.count} interactions; budget ${entry.budget}, Round 0 ${entry.round0}`).toBeLessThanOrEqual(ceiling);
      test.info().annotations.push({ type: "interactions", description: `${act.count} (budget ${entry.budget}, Round 0 ${entry.round0})` });
    });
  }
});
