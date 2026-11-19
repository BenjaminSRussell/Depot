import { expect, test, type Page } from "@playwright/test";
import { scanRoute } from "./axe";
import { ShellPage } from "./shell.page";

const LE = "0195f0a0-0000-7000-8000-0000000000e1";
const VENDOR = "0195f0a0-0000-7000-8000-0000000000e2";
const LEDGER = "0195f0a0-0000-7000-8000-0000000000e3";
const RECEIPT = "0195f0a0-0000-7000-8000-0000000000e4";
const CHANGE = "0195f0a0-0000-7000-8000-0000000000e5";
const PROMPT_NAME = "Ask or do anything...";

const unavailable = {
  code: "AGT-0202",
  detail: "The assistant is switched off.",
};

async function stubAiDown(page: Page, mode: "503" | "timeout") {
  const down = (route: Parameters<Parameters<Page["route"]>[1]>[0]) =>
    mode === "503"
      ? route.fulfill({ status: 503, json: unavailable })
      : route.abort("timedout");
  await page.route("**/api/v1/agents/**", down);
  await page.route("**/api/v1/copilot/**", down);
}

async function pick(page: Page, name: string, option: string) {
  await page.getByRole("combobox", { name }).click();
  await page.getByRole("option", { name: option }).click();
}

for (const mode of ["503", "timeout"] as const) {
  test.describe(`AI layer down (${mode})`, () => {
    test("the classic home shows, an invoice posts and an inbox item is approved", async ({
      page,
    }, testInfo) => {
      await stubAiDown(page, mode);
      await page.route("**/api/v1/org/legal-entities**", (route) =>
        route.fulfill({
          json: { items: [{ id: LE, name: "Acme Inc", code: "ACME" }] },
        }),
      );
      await page.route("**/api/v1/party/vendors:search**", (route) =>
        route.fulfill({
          json: {
            items: [
              {
                id: VENDOR,
                name: "Globex",
                accountNumber: "V-1",
                legalEntityId: LE,
              },
            ],
          },
        }),
      );
      await page.route("**/api/v1/ledger/ledgers**", (route) =>
        route.fulfill({
          json: {
            items: [
              {
                id: LEDGER,
                name: "Main ledger",
                code: "GL",
                currency: "USD",
                legalEntityId: LE,
              },
            ],
          },
        }),
      );
      await page.route("**/api/v1/receiving/receipts**", (route) =>
        route.fulfill({
          json: {
            items: [
              {
                id: RECEIPT,
                receiptNumber: "RCV-1",
                legalEntityId: LE,
                currency: "USD",
              },
            ],
          },
        }),
      );
      let posted: unknown;
      await page.route("**/api/v1/receiving/invoices:post", async (route) => {
        posted = route.request().postDataJSON();
        await route.fulfill({
          status: 201,
          json: {
            id: "inv-1",
            status: "POSTED",
            vendorId: VENDOR,
            legalEntityId: LE,
            invoiceNumber: "INV-100",
            invoiceDate: "2026-11-10",
            postingDate: "2026-11-10",
            currency: "USD",
            ledgerId: LEDGER,
            fiscalPeriodId: "fp-1",
            total: "15.00",
            ledgerEntryId: "le-1",
            createdAt: "2026-11-10T10:00:00Z",
            lines: [
              {
                lineNo: 1,
                receiptId: RECEIPT,
                receiptLineNo: 1,
                qty: "6",
                unitPrice: "2.5",
                amount: "15.00",
                standardCost: false,
              },
            ],
          },
        });
      });
      let items = [
        {
          id: CHANGE,
          source: "party",
          kind: "Customer change",
          reason: "Credit limit",
          number: "CHG-1",
          link: "/party/changes",
          actions: ["approve", "reject"],
          createdAt: "2026-11-10T10:00:00Z",
        },
      ];
      await page.route("**/api/v1/inbox?**", (route) =>
        route.fulfill({ json: { items, unavailable: [], truncated: [] } }),
      );
      await page.route(
        `**/api/v1/party/pending-changes/${CHANGE}:approve`,
        (route) => {
          items = [];
          return route.fulfill({ json: {} });
        },
      );
      const shell = new ShellPage(page);

      await shell.goto("/");
      await expect(
        page.getByRole("textbox", { name: PROMPT_NAME }),
      ).toHaveCount(0);
      await scanRoute(page, testInfo, "home-ai-fallback");

      await shell.goto("/ap-invoices/post");
      await pick(page, "Legal entity", "Acme Inc");
      await pick(page, "Vendor", "Globex");
      await page.getByLabel("Invoice number").fill("INV-100");
      await page.getByLabel("Invoice date").fill("2026-11-10");
      await page.getByLabel("Posting date").fill("2026-11-10");
      await page.getByLabel("Currency").fill("USD");
      await pick(page, "Ledger", "Main ledger");
      await pick(page, "Line 1 receipt", "RCV-1");
      await page.getByLabel("Line 1 quantity").fill("6");
      await page.getByLabel("Line 1 unit price").fill("2.5");
      await page.getByRole("button", { name: "Post invoice" }).click();
      await expect(page.getByText("Invoice INV-100 posted")).toBeVisible();
      expect(posted).toMatchObject({
        invoiceNumber: "INV-100",
        vendorId: VENDOR,
      });

      await shell.goto("/workflow/inbox");
      await page
        .getByRole("button", { name: "Approve Customer change CHG-1" })
        .click();
      await page.getByRole("button", { name: "Confirm approval" }).click();
      await expect(page.getByText("Approved: CHG-1.")).toBeVisible();
      await expect(page.getByRole("alert")).toHaveCount(0);
      await expect(page.getByRole("dialog")).toHaveCount(0);
    });
  });
}
