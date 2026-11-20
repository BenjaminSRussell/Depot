import type { Page } from "@playwright/test";
import { expect, test } from "./_shared/writeGuard";
import { scanRoute } from "./axe";
import { ShellPage } from "./shell.page";

const APPROVAL_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11";
const BRIEFINGS = /\/api\/v1\/agents\/briefings(\?|$)/;

const AVAILABLE = {
  killSwitch: { halted: false, stale: false, tenant: { active: false, version: null, changedAt: null, byUser: null }, global: { active: false, version: null, changedAt: null } },
  budgetStatus: "OK",
  copilotEnabled: true,
};

const briefing = (narrative: boolean) => ({
  id: "0b9a6b5e-2f4c-4f6e-9c1d-3d2b1a0f9e88",
  status: "READY",
  narrative,
  createdAt: "2026-11-19T10:00:00.000Z",
  completedAt: "2026-11-19T10:00:05.000Z",
  readAt: null,
  changes: [{ agentId: "ap_coding", factId: "f0", text: "3 invoices were coded" }],
  risks: [{ agentId: "anomaly", factId: "f1", text: "One journal looks unusual" }],
  recommendedActions: [
    { agentId: "cash_app", factId: "f2", text: "Apply the 4,200.00 USD payment from Acme", actionId: APPROVAL_ID, route: `/assistant/proposals/${APPROVAL_ID}` },
  ],
  notes: [],
});

async function stubBriefings(page: Page, narrative: boolean) {
  await page.route(BRIEFINGS, (route) => route.fulfill({ json: { items: [briefing(narrative)], nextCursor: null, nextScheduledAt: null } }));
}

test.beforeEach(async ({ page }) => {
  await page.route("**/api/v1/agents/availability", (route) => route.fulfill({ json: AVAILABLE }));
});

for (const colorScheme of ["light", "dark"] as const) {
  for (const [label, viewport] of [
    ["mobile", { width: 375, height: 812 }],
    ["desktop", { width: 1280, height: 800 }],
  ] as const) {
    test.describe(`briefing card, ${colorScheme}, ${label}`, () => {
      test.use({ colorScheme, viewport });

      test("has no axe violations with a narrative briefing", async ({ page }, testInfo) => {
        await stubBriefings(page, true);
        await new ShellPage(page).goto("/");
        await expect(page.getByRole("heading", { level: 2, name: "Your briefing" })).toBeVisible();
        await expect(page.getByRole("button", { name: "Recommended actions (1)" })).toHaveAttribute("aria-expanded", "true");
        await scanRoute(page, testInfo, "home-briefing-narrative");
      });

      test("has no axe violations with a deterministic briefing", async ({ page }, testInfo) => {
        await stubBriefings(page, false);
        await new ShellPage(page).goto("/");
        await expect(page.getByText("Summaries are unavailable")).toBeVisible();
        await scanRoute(page, testInfo, "home-briefing-deterministic");
      });

      test("has no axe violations when the briefing cannot be loaded", async ({ page }, testInfo) => {
        await page.route(BRIEFINGS, (route) => route.fulfill({ status: 500, json: { code: "AGT-0500" } }));
        await new ShellPage(page).goto("/");
        await expect(page.getByRole("alert").filter({ hasText: "Your briefing could not be loaded" })).toBeVisible();
        await scanRoute(page, testInfo, "home-briefing-error");
      });
    });
  }
}

test.describe("briefing card", () => {
  test("shows the narrative briefing with source agents and the AI badge", async ({ page, writeGuard }) => {
    await stubBriefings(page, true);
    await new ShellPage(page).goto("/");
    await expect(page.getByText("AI-generated: check before you use it")).toBeVisible();
    await expect(page.getByText("Cash application agent:")).toBeVisible();
    await page.getByRole("button", { name: "Risks (1)" }).click();
    await expect(page.getByText("One journal looks unusual")).toBeVisible();
    writeGuard.expectNoWrites();
  });

  test("follows a recommended action to the proposal route and writes nothing", async ({ page, writeGuard }) => {
    await stubBriefings(page, true);
    await page.route(`**/api/v1/agents/approvals/${APPROVAL_ID}`, (route) =>
      route.fulfill({
        json: {
          runId: "run-1",
          actionId: APPROVAL_ID,
          tool: "depot.receivables.payment.propose_apply",
          state: "PENDING",
          amount: { amount: "4200.00", currency: "USD" },
          requestedAt: "2026-11-19T10:00:00.000Z",
          expiresAt: "2026-11-20T10:00:00.000Z",
        },
      }),
    );
    await new ShellPage(page).goto("/");
    await expect(page.getByRole("button", { name: /^(approve|decline)/i })).toHaveCount(0);
    await page.getByRole("link", { name: "Apply the 4,200.00 USD payment from Acme" }).click();
    await expect(page).toHaveURL(new RegExp(`/assistant/proposals/${APPROVAL_ID}$`));
    await expect(page.getByRole("heading", { level: 2, name: "Assistant proposal" })).toBeVisible();
    writeGuard.expectNoWrites();
  });

  test("shows counts and a note, and no AI badge, for a deterministic briefing", async ({ page }) => {
    await stubBriefings(page, false);
    await new ShellPage(page).goto("/");
    await expect(page.getByText("Summaries are unavailable, so this briefing lists counts and links only.")).toBeVisible();
    await expect(page.getByRole("button", { name: "Changes (1)" })).toBeVisible();
    await expect(page.getByText("AI-generated: check before you use it")).toHaveCount(0);
  });

  test("shows an inline error with Try again and leaves the rest of the home working", async ({ page }) => {
    let failing = true;
    await page.route(BRIEFINGS, (route) =>
      failing
        ? route.fulfill({ status: 500, json: { code: "AGT-0500" } })
        : route.fulfill({ json: { items: [briefing(true)], nextCursor: null, nextScheduledAt: null } }),
    );
    const shell = new ShellPage(page);
    await shell.goto("/");
    await expect(page.getByText("Your briefing could not be loaded")).toBeVisible();
    await expect(page.getByRole("textbox", { name: "Ask or do anything..." })).toBeVisible();
    await expect(page.getByRole("dialog")).toHaveCount(0);
    failing = false;
    await page.getByRole("alert").filter({ hasText: "Your briefing could not be loaded" }).getByRole("button", { name: "Try again" }).click();
    await expect(page.getByRole("button", { name: "Recommended actions (1)" })).toBeVisible();
  });
});
