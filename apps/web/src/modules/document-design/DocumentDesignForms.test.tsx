import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, pageViolations, renderRoutes, server, useFakeServer } from "../../shell/testing/fakeServer";
import documentDesign from "./module";
import { validateRule, blankRule } from "./OutputRuleFormPage";
import { blankSchedule, validateSchedule } from "./ScheduleFormPage";
import { blankTemplate, validateTemplate } from "./TemplateFormPage";
import { placeholdersOf, validateBlock } from "./TextBlockFormPage";

useFakeServer();

const LE = "0190a5f2-7b1c-7d3e-8a4f-123456789a01";
const template = { code: "INV-STD", name: "Standard invoice", documentType: "CUSTOMER_INVOICE", default: true, version: 3, config: { title: "Tax invoice", hideColumns: ["Unit"], noteBlocks: ["TERMS"] } };
const block = { code: "TERMS", language: "en", body: "Pay within {{days}} days.", placeholders: ["days"], version: 1 };

const fill = (label: string | RegExp, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });
const location = () => screen.getByTestId("location").textContent;
const sent = (method: string) => server.calls.filter((c) => c.method === method);

describe("validation mirrors the service", () => {
  it("checks a template", () => {
    expect(validateTemplate(blankTemplate, false).errors.code).toMatch(/capital letters/);
    const v = { ...blankTemplate, code: "INV-NEW", name: "New", renameColumns: "Qty - Quantity", hideColumns: "a\nb\nc\nd\ne\nf\ng\nh\ni" };
    const { errors } = validateTemplate(v, false);
    expect(errors.renameColumns).toMatch(/Current header = New name/);
    expect(errors.hideColumns).toMatch(/up to 8/);
    expect(validateTemplate({ ...blankTemplate, code: "X1", name: "n", bodyBlock: "COVER" }, false).errors.bodyBlock).toMatch(/Only statements/);
    const ok = validateTemplate({ ...blankTemplate, code: "INV-NEW", name: " New ", title: "Invoice", renameColumns: "Qty = Quantity\nUnit = Per", hideColumns: "Tax\n", noteBlocks: ["TERMS"] }, false);
    expect(ok.errors).toEqual({});
    expect(ok.input).toEqual({
      name: "New",
      documentType: "CUSTOMER_INVOICE",
      default: false,
      config: { title: "Invoice", renameColumns: { Qty: "Quantity", Unit: "Per" }, hideColumns: ["Tax"], noteBlocks: ["TERMS"] },
    });
    // The code of a saved template is not checked again.
    expect(validateTemplate({ ...blankTemplate, code: "old", name: "n" }, true).errors.code).toBeUndefined();
  });

  it("checks a text block, a rule and a schedule", () => {
    expect(validateBlock({ code: "bad", language: "english", body: " " }, false)).toEqual({ code: expect.any(String), language: expect.any(String), body: "Enter the text." });
    expect(placeholdersOf("Due {{ dueDate }} and {{amount}} and {{dueDate}}")).toEqual(["amount", "dueDate"]);
    expect(validateRule({ ...blankRule, destination: "not an address" }).errors.destination).toBe("Enter one e-mail address.");
    expect(validateRule({ ...blankRule, channel: "PRINTER", destination: "OFFICE", copies: "10" }).errors.copies).toBe("Enter 1 to 9 copies.");
    expect(validateRule({ ...blankRule, channel: "PORTAL", copies: "2" }).errors.copies).toMatch(/Only a printer/);
    expect(validateRule({ ...blankRule, channel: "ARCHIVE" }).input).toEqual({ documentType: "CUSTOMER_INVOICE", copyKind: "ORIGINAL", channel: "ARCHIVE", copies: 1, sequence: 1 });
    expect(validateSchedule(blankSchedule).errors).toMatchObject({ name: expect.any(String), legalEntityId: expect.any(String), senderName: expect.any(String) });
    expect(validateSchedule({ ...blankSchedule, name: "Monthly", legalEntityId: LE, senderName: "Cedar Ridge", runDay: "29" }).errors.runDay).toMatch(/1 to 28/);
    expect(validateSchedule({ ...blankSchedule, name: "Weekly", legalEntityId: LE, senderName: "Cedar Ridge", cadence: "WEEKLY", runDay: "8" }).errors.runDay).toBe("Choose a weekday.");
  });
});

describe("Document template form", () => {
  it("creates a template without If-Match and opens it", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT" && url.pathname.endsWith("/document-templates/INV-NEW")) return json(200, { ...template, code: "INV-NEW", name: "New invoice", version: 1 });
      if (url.pathname.endsWith("/text-blocks")) return json(200, { items: [block] });
      if (url.pathname.endsWith("/document-templates/INV-NEW")) return json(200, { ...template, code: "INV-NEW", name: "New invoice", version: 1 });
      return undefined;
    };
    const { container } = renderRoutes(documentDesign.routes, "/document-design/templates/new");
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(screen.getByRole("button", { name: "Create template" }));
    expect((await screen.findAllByText(/capital letters, digits/)).length).toBeGreaterThan(0);
    expect(sent("PUT")).toHaveLength(0);
    fill("Code", "inv-new");
    fill("Name", "New invoice");
    fill("Title", "Tax invoice");
    fireEvent.click(screen.getByRole("button", { name: "Create template" }));
    await waitFor(() => expect(location()).toBe("/document-design/templates/INV-NEW"));
    const put = sent("PUT")[0]!;
    expect(new Headers(put.init.headers).get("If-Match")).toBeNull();
    expect(put.body).toEqual({ name: "New invoice", documentType: "CUSTOMER_INVOICE", default: false, config: { title: "Tax invoice" } });
  });

  it("saves an edit at the opened version and says plainly when someone else saved first", async () => {
    let status = 200;
    server.handler = (url, init) => {
      if (url.pathname.endsWith("/text-blocks")) return json(200, { items: [block] });
      if (init.method === "PUT") return status === 200 ? json(200, { ...template, version: 4 }) : json(status, { code: status === 412 ? "PRN-0017" : "PRN-0018", detail: "stale" });
      if (url.pathname.endsWith("/document-templates/INV-STD")) return json(200, template);
      return undefined;
    };
    renderRoutes(documentDesign.routes, "/document-design/templates/INV-STD");
    const title = await screen.findByLabelText("Title");
    expect(title).toHaveValue("Tax invoice");
    expect(screen.getByLabelText("Code")).toHaveAttribute("readonly");
    fill("Title", "Invoice");
    fireEvent.click(screen.getByRole("button", { name: "Save template" }));
    expect(await screen.findByText("Saved as version 4.")).toBeInTheDocument();
    expect(new Headers(sent("PUT")[0]!.init.headers).get("If-Match")).toBe('"3"');
    expect(screen.getByRole("link", { name: "Version history" })).toHaveAttribute("href", "/document-design/templates/INV-STD/versions");
    status = 412;
    fireEvent.click(screen.getByRole("button", { name: "Save template" }));
    expect(await screen.findByText(/Someone else saved this document template after you opened it\./)).toBeInTheDocument();
    expect(screen.getByText("This template changed while you were editing")).toBeInTheDocument();
    status = 428;
    fireEvent.click(screen.getByRole("button", { name: "Save template" }));
    expect(await screen.findByText(/This document template already exists, so a save has to say which version it changes\./)).toBeInTheDocument();
  });
});

describe("Template version history", () => {
  const versions = [
    { ...template, version: 3, savedBy: "dev", savedAt: "2026-11-10T09:30:00Z" },
    { ...template, version: 2, config: { title: "Invoice" }, savedBy: "dev", savedAt: "2026-11-01T09:30:00Z" },
    { ...template, version: 1, config: {}, savedAt: "2026-10-01T09:30:00Z" },
  ];
  const base = (url: URL) => {
    if (url.pathname.endsWith("/versions")) return json(200, { items: versions });
    if (url.pathname.endsWith("/document-templates/INV-STD")) return json(200, template);
    return undefined;
  };

  it("lists the versions and restores an earlier one as a new version with If-Match", async () => {
    server.handler = (url, init) => (init.method === "POST" ? json(200, { ...template, version: 4, config: {} }) : base(url));
    const { container } = renderRoutes(documentDesign.routes, "/document-design/templates/INV-STD/versions");
    const table = await screen.findByRole("table", { name: "Template versions" });
    expect(within(table).getByText("Current")).toBeInTheDocument();
    expect(within(table).queryByRole("button", { name: "Restore version 3" })).toBeNull();
    expect(within(table).getByText("Before history was kept")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
    fireEvent.click(within(table).getByRole("button", { name: "Restore version 1" }));
    expect(screen.getByText(/saved as version 4/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Restore" }));
    expect(await screen.findByText("Version 1 was restored as version 4.")).toBeInTheDocument();
    const post = sent("POST")[0]!;
    expect(post.url.pathname).toBe("/api/v1/print/document-templates/INV-STD/versions/1:restore");
    expect(new Headers(post.init.headers).get("If-Match")).toBe('"3"');
  });

  it("says plainly when the template changed after the page opened", async () => {
    server.handler = (url, init) => (init.method === "POST" ? json(412, { code: "PRN-0017", detail: "stale" }) : base(url));
    renderRoutes(documentDesign.routes, "/document-design/templates/INV-STD/versions");
    fireEvent.click(await screen.findByRole("button", { name: "Restore version 2" }));
    fireEvent.click(screen.getByRole("button", { name: "Restore" }));
    expect(await screen.findByText(/Someone else saved this document template after you opened it\./)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Reload" })).toBeInTheDocument();
  });
});

describe("Text block form", () => {
  it("creates a text block and shows what it fills in", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT") return json(200, { ...block, code: "COVER", version: 1 });
      if (url.pathname.endsWith("/text-blocks")) return json(200, { items: [{ ...block, code: "COVER" }] });
      return undefined;
    };
    renderRoutes(documentDesign.routes, "/document-design/text-blocks/new");
    fill("Code", "cover");
    fill("Text", "Dear {{counterpartyName}}, due {{dueDate}}.");
    expect(screen.getByText("Fills in: counterpartyName, dueDate.")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Create text block" }));
    await waitFor(() => expect(location()).toBe("/document-design/text-blocks/COVER/en"));
    expect(sent("PUT")[0]!.url.pathname).toBe("/api/v1/print/text-blocks/COVER/en");
    expect(sent("PUT")[0]!.body).toEqual({ body: "Dear {{counterpartyName}}, due {{dueDate}}." });
  });

  it("shows the service's reason for a refused text", async () => {
    server.handler = (url, init) => {
      if (init.method === "PUT") return json(400, { code: "COM-0001", detail: "request failed validation", errors: { body: "a default template may only use the built-in placeholders" } });
      if (url.pathname.endsWith("/text-blocks")) return json(200, { items: [block] });
      return undefined;
    };
    renderRoutes(documentDesign.routes, "/document-design/text-blocks/TERMS/en");
    expect(await screen.findByLabelText("Text")).toHaveValue(block.body);
    fireEvent.click(screen.getByRole("button", { name: "Save text block" }));
    expect((await screen.findAllByText(/built-in placeholders/)).length).toBeGreaterThan(0);
  });
});

describe("Output rule form", () => {
  it("adds a rule with an Idempotency-Key and returns to the list", async () => {
    server.handler = (url, init) => (init.method === "POST" ? json(201, { id: "r1" }) : url.pathname.endsWith("/statement-schedules") ? json(200, { items: [] }) : json(200, { items: [] }));
    renderRoutes(documentDesign.routes, "/document-design/output-rules/new");
    fill("Goes to", "PRINTER");
    fill("Print queue", "office");
    fill("Copies", "2");
    fireEvent.click(screen.getByRole("button", { name: "Add output rule" }));
    await waitFor(() => expect(location()).toBe("/document-design"));
    const post = sent("POST")[0]!;
    expect(new Headers(post.init.headers).get("Idempotency-Key")).toMatch(/[0-9a-f-]{36}/);
    expect(post.body).toEqual({ documentType: "CUSTOMER_INVOICE", copyKind: "ORIGINAL", channel: "PRINTER", destination: "OFFICE", copies: 2, sequence: 1 });
  });

  it("removes a rule after asking", async () => {
    const rule = { id: "0b7e1f84-0000-4000-8000-000000000001", documentType: "CUSTOMER_INVOICE", copyKind: "COPY", channel: "ARCHIVE", copies: 1, sequence: 1 };
    server.handler = (url, init) => {
      if (init.method === "DELETE") return json(200, {});
      if (url.pathname.endsWith("/output-rules")) return json(200, { items: [rule] });
      return json(200, { items: [] });
    };
    renderRoutes(documentDesign.routes, "/document-design");
    fireEvent.click(await screen.findByRole("button", { name: "Remove Customer invoice rule" }));
    expect(screen.getByText("Remove this output rule?")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Remove" }));
    expect(await screen.findByText("The output rule was removed.")).toBeInTheDocument();
    expect(sent("DELETE")[0]!.url.pathname).toBe(`/api/v1/print/output-rules/${rule.id}`);
  });
});

describe("Statement schedules", () => {
  const schedule = { id: "0b7e1f84-0000-4000-8000-0000000000aa", name: "Monthly statements", legalEntityId: LE, senderName: "Cedar Ridge Pumps", cadence: "MONTHLY", runDay: 3, language: "en", enabled: true, nextRunOn: "2026-12-03" };

  it("creates a schedule", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(201, schedule);
      if (url.pathname.endsWith("/document-templates")) return json(200, { items: [{ ...template, code: "STMT", name: "Statement", documentType: "PERIODIC_STATEMENT" }, template] });
      return json(200, { items: [] });
    };
    renderRoutes(documentDesign.routes, "/document-design/statement-schedules/new");
    fireEvent.click(screen.getByRole("button", { name: "Add schedule" }));
    expect(await screen.findByText("Enter a name of up to 100 characters.")).toBeInTheDocument();
    fill("Name", "Monthly statements");
    fill(/Legal entity/, LE);
    fill("Name on the statements", "Cedar Ridge Pumps");
    fireEvent.click(screen.getByRole("button", { name: "Add schedule" }));
    await waitFor(() => expect(location()).toBe("/document-design"));
    expect(sent("POST")[0]!.body).toEqual({ name: "Monthly statements", legalEntityId: LE, senderName: "Cedar Ridge Pumps", cadence: "MONTHLY", runDay: 3, language: "en" });
    expect(new Headers(sent("POST")[0]!.init.headers).get("Idempotency-Key")).toBeTruthy();
  });

  it("pauses a schedule and runs it now", async () => {
    let enabled = true;
    server.handler = (url, init) => {
      if (init.method === "PATCH") {
        enabled = (JSON.parse(String(init.body)) as { enabled: boolean }).enabled;
        return json(200, { ...schedule, enabled });
      }
      if (init.method === "POST" && url.pathname.endsWith(":run")) return json(200, { scheduleId: schedule.id, asOf: "2026-11-15", delivered: 4, skipped: 1, failed: 0 });
      if (url.pathname.endsWith("/statement-schedules")) return json(200, { items: [{ ...schedule, enabled }] });
      return json(200, { items: [] });
    };
    renderRoutes(documentDesign.routes, "/document-design");
    const table = await screen.findByRole("table", { name: "Statement schedules" });
    expect(within(table).getByText("Every month on day 3")).toBeInTheDocument();
    fireEvent.click(within(table).getByRole("button", { name: "Run Monthly statements now" }));
    expect(await screen.findByText(/ran for Nov 15, 2026: 4 delivered, 1 skipped, 0 failed/)).toBeInTheDocument();
    fireEvent.click(within(table).getByRole("button", { name: "Pause Monthly statements" }));
    expect(await screen.findByText("Monthly statements paused.")).toBeInTheDocument();
    expect(await within(await screen.findByRole("table", { name: "Statement schedules" })).findByText("Paused")).toBeInTheDocument();
  });

  it("says when statements cannot be read", async () => {
    server.handler = (url, init) => {
      if (init.method === "POST") return json(502, { code: "PRN-0021", detail: "receivables answered 503" });
      if (url.pathname.endsWith("/statement-schedules")) return json(200, { items: [schedule] });
      return json(200, { items: [] });
    };
    renderRoutes(documentDesign.routes, "/document-design");
    fireEvent.click(await screen.findByRole("button", { name: "Run Monthly statements now" }));
    expect(await screen.findByText(/The customers' statements couldn't be read from receivables\./)).toBeInTheDocument();
  });
});
