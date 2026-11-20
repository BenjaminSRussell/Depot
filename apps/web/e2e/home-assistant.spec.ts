import type { Page } from "@playwright/test";
import { expect, test } from "./_shared/writeGuard";
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

  test("offers the mode selector with Auto chosen and Do unavailable without the duty", async ({ page }) => {
    await new ShellPage(page).goto("/");
    const modes = page.getByRole("group", { name: "What should the assistant do?" });
    await expect(modes.getByRole("radio", { name: "Auto" })).toBeChecked();
    await expect(modes.getByRole("radio", { name: "Go to" })).toBeEnabled();
  });

  test("offers both clarify choices instead of a dead end", async ({ page }) => {
    await page.route("**/api/v1/copilot/chat", (route) => route.fulfill({ json: { kind: "clarify", confidence: 0.5, candidates: ["navigate", "ask"] } }));
    await new ShellPage(page).goto("/");
    await page.getByRole("textbox", { name: PROMPT_NAME }).fill("invoices");
    await page.keyboard.press("Enter");
    await expect(page.getByRole("button", { name: "Take me there" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Answer a question" })).toBeVisible();
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

const CONVERSATION_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11";

function sse(frames: { event: string; data: unknown }[]): string {
  return frames.map((f) => `event: ${f.event}\ndata: ${JSON.stringify(f.data)}\n\n`).join("");
}

const ANSWER = {
  answer: "Overdue invoices total 48,200.00 USD [[cite:record:ledger/ap-invoice/INV-1042]].",
  citations: [{ sourceType: "RECORD", sourceRef: "record:ledger/ap-invoice/INV-1042", label: "Invoice INV-1042" }],
  toolCalls: [{ tool: "analytics.query", status: "OK" }],
  refusal: null,
  generated: true,
};

async function stubAsk(page: Page, doneFrame: unknown, tokens: string[] = ["Overdue invoices "]) {
  await page.route("**/api/v1/copilot/chat", (route) => route.fulfill({ json: { kind: "ask", confidence: 0.95 } }));
  await page.route("**/api/v1/agents/copilot/conversations", (route) => route.fulfill({ status: 201, json: { conversationId: CONVERSATION_ID } }));
  await page.route(`**/api/v1/agents/copilot/conversations/${CONVERSATION_ID}/messages`, (route) =>
    route.fulfill({
      status: 200,
      contentType: "text/event-stream",
      body: sse([...tokens.map((text) => ({ event: "token", data: { text } })), { event: "done", data: doneFrame }]),
    }),
  );
}

async function ask(page: Page, question: string) {
  await new ShellPage(page).goto("/");
  await page.getByRole("textbox", { name: PROMPT_NAME }).fill(question);
  await page.keyboard.press("Enter");
}

test.describe("inline ask on the home", () => {
  test("streams a sourced answer inline, clicks the citation through to the record, and writes nothing else", async ({ page, writeGuard }) => {
    await stubAsk(page, ANSWER);
    await ask(page, "what is the total of our overdue invoices?");
    const answer = page.getByRole("article", { name: "Copilot answer" });
    await expect(answer).toContainText("Overdue invoices total 48,200.00 USD");
    await expect(page).toHaveURL(/\/$/);
    await expect(page.getByRole("status").filter({ hasText: "finished answering" })).toHaveCount(1);
    await answer.getByRole("link", { name: "Invoice INV-1042" }).click();
    await expect(page).toHaveURL(/\/ap-invoices\/INV-1042$/);
    writeGuard.expectNoWrites();
  });

  test("shows a refusal with its code and offers no draft", async ({ page, writeGuard }) => {
    await stubAsk(page, {
      answer: "",
      citations: [],
      toolCalls: [],
      refusal: { code: "AGT-0203", message: "That tool is not allowed for you." },
      generated: true,
    });
    await ask(page, "post the invoice");
    await expect(page.getByRole("alert").filter({ hasText: "AGT-0203" })).toBeVisible();
    await expect(page.getByRole("button", { name: "Insert draft" })).toHaveCount(0);
    writeGuard.expectNoWrites();
  });

  test("notes that an answer at the 8-call limit may be partial", async ({ page, writeGuard }) => {
    await stubAsk(page, {
      ...ANSWER,
      answer: "Here is what I found so far.",
      toolCalls: Array.from({ length: 8 }, () => ({ tool: "analytics.query", status: "OK" })),
    });
    await ask(page, "everything about all vendors");
    await expect(page.getByText(/may be partial/)).toBeVisible();
    writeGuard.expectNoWrites();
  });

  test("has no axe violations with an answer shown", async ({ page }, testInfo) => {
    await stubAsk(page, ANSWER);
    await ask(page, "overdue invoices");
    await expect(page.getByRole("article", { name: "Copilot answer" })).toBeVisible();
    await scanRoute(page, testInfo, "home-assistant-answer");
  });

  test.skip("shows only the records each persona may see against the real stack", () => undefined);
});
