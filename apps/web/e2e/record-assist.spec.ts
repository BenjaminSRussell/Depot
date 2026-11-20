import type { Page } from "@playwright/test";
import { expect, test } from "./_shared/writeGuard";
import { scanRoute } from "./axe";
import { ShellPage } from "./shell.page";

const PO = "0195f0a0-0000-7000-8000-000000000071";
const VENDOR = "0195f0a0-0000-7000-8000-000000000072";
const LE = "00000000-0000-0000-0000-0000000000e1";
const CONVERSATION_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11";
const BUTTON = "Ask about this record";

const availability = (copilotEnabled: boolean) => ({
  killSwitch: { halted: false, stale: false, tenant: { active: false, version: null, changedAt: null, byUser: null }, global: { active: false, version: null, changedAt: null } },
  budgetStatus: "OK",
  copilotEnabled,
});

const READ_ALL = ["depot.platform.audit_trail.get", "depot.platform.document_flow.get", "depot.workflow.history.get"].map((tool) => ({ tool, status: "OK" }));

const ANSWERS: Record<string, unknown> = {
  Explain: {
    answer: "This purchase order is confirmed and waits for goods [[cite:record:ledger/ap-invoice/INV-1042]].",
    citations: [{ sourceType: "RECORD", sourceRef: "record:ledger/ap-invoice/INV-1042", label: "Invoice INV-1042" }],
    toolCalls: READ_ALL,
    refusal: null,
    generated: true,
  },
  List: {
    answer: "There is no change history for this record.",
    citations: [],
    toolCalls: [{ tool: "depot.platform.audit_trail.get", status: "OK" }],
    refusal: null,
    generated: true,
  },
  What: {
    answer: "Record the arrival of the goods.",
    citations: [],
    toolCalls: READ_ALL,
    refusal: null,
    generated: true,
  },
};

function sse(done: unknown): string {
  return `event: done\ndata: ${JSON.stringify(done)}\n\n`;
}

async function stubRecordPage(page: Page, copilotEnabled = true) {
  await page.route("**/api/v1/agents/availability", (route) => route.fulfill({ json: availability(copilotEnabled) }));
  await page.route("**/api/v1/party/**", (route) => route.fulfill({ status: 404, json: { code: "COM-0404", detail: "none" } }));
  await page.route(`**/api/v1/procurement/purchase-orders/${PO}`, (route) =>
    route.fulfill({
      json: { id: PO, number: "PO-1042", legalEntityId: LE, vendorId: VENDOR, currency: "USD", approvalStatus: "CONFIRMED", documentStatus: "OPEN", versionNo: 0, changeManagement: false, subtotal: "0", discountTotal: "0", chargesTotal: "0", taxTotal: "0", total: "0", rowVersion: 1, lines: [], charges: [], createdAt: "2026-11-10T10:00:00Z", updatedAt: "2026-11-10T10:00:00Z" },
      headers: { ETag: '"1"' },
    }),
  );
  await page.route("**/api/v1/procurement/purchase-orders/*/**", (route) => route.fulfill({ json: { items: [] } }));
  await page.route("**/api/v1/receiving/**", (route) => route.fulfill({ json: { items: [] } }));
  await page.route("**/api/v1/receiving/related-documents**", (route) => route.fulfill({ json: { purchaseOrderId: PO, receipts: [], invoices: [], truncated: false } }));
  await page.route("**/api/v1/agents/copilot/conversations", (route) => route.fulfill({ status: 201, json: { conversationId: CONVERSATION_ID } }));
  await page.route(`**/api/v1/agents/copilot/conversations/${CONVERSATION_ID}/messages`, (route) => {
    const { message } = route.request().postDataJSON() as { message: string };
    const key = Object.keys(ANSWERS).find((prefix) => message.startsWith(prefix)) ?? "Explain";
    return route.fulfill({ status: 200, contentType: "text/event-stream", body: sse(ANSWERS[key]) });
  });
}

async function openPanel(page: Page) {
  await new ShellPage(page).goto(`/purchasing/purchase-orders/${PO}`);
  await page.getByRole("button", { name: BUTTON }).click();
  const dialog = page.getByRole("dialog", { name: "Ask about this purchase order" });
  await expect(dialog).toBeVisible();
  return dialog;
}

test.describe("ask about this record", () => {
  test("runs all three actions on a purchase order, follows a citation and writes nothing", async ({ page, writeGuard }) => {
    await stubRecordPage(page);
    const dialog = await openPanel(page);

    await dialog.getByRole("button", { name: "Explain this record" }).click();
    const answer = dialog.getByRole("article", { name: "Copilot answer" });
    await expect(answer).toContainText("This purchase order is confirmed");
    await expect(answer.getByText("AI-generated")).toBeVisible();
    await expect(answer.getByText("Tools called")).toBeAttached();

    await dialog.getByRole("button", { name: "What changed" }).click();
    await expect(answer).toHaveCount(2);
    await expect(answer.last()).toContainText("There is no change history for this record.");
    await expect(dialog.getByText("Part of this record could not be read")).toHaveCount(0);

    await dialog.getByRole("button", { name: "What should I do next" }).click();
    await expect(dialog.getByRole("region", { name: "Where to go next" }).getByRole("link").first()).toBeVisible();
    await expect(dialog.getByRole("button", { name: "Insert draft" })).toHaveCount(0);

    await dialog.getByRole("button", { name: "Explain this record" }).click();
    await expect(answer).toHaveCount(4);
    await dialog.getByRole("link", { name: "Invoice INV-1042" }).last().click();
    await expect(page).toHaveURL(/\/ap-invoices\/INV-1042$/);
    writeGuard.expectNoWrites();
  });

  test("closes with Escape and returns focus to the button", async ({ page }) => {
    await stubRecordPage(page);
    const dialog = await openPanel(page);
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
    await expect(page.getByRole("button", { name: BUTTON })).toBeFocused();
  });

  test("has no axe violations with the panel open and an answer shown", async ({ page }, testInfo) => {
    await stubRecordPage(page);
    const dialog = await openPanel(page);
    await dialog.getByRole("button", { name: "Explain this record" }).click();
    await expect(dialog.getByRole("article", { name: "Copilot answer" })).toBeVisible();
    await scanRoute(page, testInfo, "record-assist-panel");
  });

  test("leaves the record page unchanged and shows no button while AI is off", async ({ page, writeGuard }) => {
    await stubRecordPage(page, false);
    await new ShellPage(page).goto(`/purchasing/purchase-orders/${PO}`);
    await expect(page.getByRole("heading", { name: "Purchase order PO-1042" })).toBeVisible();
    await expect(page.getByRole("button", { name: BUTTON })).toHaveCount(0);
    writeGuard.expectNoWrites();
  });

  test.skip("reads the audit trail, document flow and workflow history against the real stack", () => undefined);
});
