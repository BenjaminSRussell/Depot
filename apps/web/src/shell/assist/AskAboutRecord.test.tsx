import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import type { ChatResponse } from "../../modules/copilot/api/client";
import { pageViolations } from "../../modules/copilot/a11yTest";
import { GENERATED_BADGE } from "../../modules/copilot/domain";
import type { AskFn } from "../../modules/copilot/useCopilotConversation";
import { AvailabilityProvider, type AiAvailability } from "../assistant/AvailabilityProvider";
import { copy } from "../copy";
import { PrincipalProvider } from "../principal";
import { PageHeader } from "../ui";
import { AskAboutRecord } from "./AskAboutRecord";
import source from "./AskAboutRecord.tsx?raw";
import { AUDIT_TRAIL_TOOL, DOCUMENT_FLOW_TOOL, WORKFLOW_HISTORY_TOOL } from "./recordAssist";

const PO = "0195f0a0-0000-7000-8000-000000000071";
const COPILOT = "mcp.copilot.use";
const ENABLED: AiAvailability = { state: { status: "enabled", budget: "OK" }, reportRefusal: () => undefined, reportFailure: () => undefined };
const ALL_TOOLS = [AUDIT_TRAIL_TOOL, DOCUMENT_FLOW_TOOL, WORKFLOW_HISTORY_TOOL].map((tool) => ({ tool, status: "OK" as const }));

const explainAnswer: ChatResponse = {
  answer: "This purchase order is confirmed and has one receipt [[cite:record:ledger/purchase-order/PO-1042]].",
  citations: [{ sourceType: "RECORD", sourceRef: "record:ledger/purchase-order/PO-1042", label: "Purchase order PO-1042" }],
  toolCalls: ALL_TOOLS,
  refusal: null,
  generated: true,
};

function stub(response: ChatResponse | Error) {
  return vi.fn<AskFn>(async () => {
    if (response instanceof Error) throw response;
    return response;
  });
}

function renderControl(ask: AskFn, { duties = [COPILOT], availability = ENABLED, entity = "purchase-order", id = PO } = {}) {
  return render(
    <MemoryRouter>
      <PrincipalProvider duties={duties}>
        <AvailabilityProvider value={availability}>
          <AskAboutRecord entity={entity} id={id} ask={ask} createConversation={async () => "conversation-1"} />
        </AvailabilityProvider>
      </PrincipalProvider>
    </MemoryRouter>,
  );
}

const openPanel = async () => {
  const user = userEvent.setup();
  await user.click(screen.getByRole("button", { name: copy.recordAssist.button }));
  return user;
};

const choose = async (user: ReturnType<typeof userEvent.setup>, label: string) => {
  await user.click(screen.getByRole("button", { name: label }));
};

describe("AskAboutRecord gating", () => {
  it("shows the button for an opted-in record when AI is enabled and the user holds the copilot duty", () => {
    renderControl(stub(explainAnswer));
    expect(screen.getByRole("button", { name: copy.recordAssist.button })).toBeInTheDocument();
  });

  it("renders nothing without the copilot duty", () => {
    renderControl(stub(explainAnswer), { duties: ["procurement.purchase-order.inquire"] });
    expect(screen.queryByRole("button")).toBeNull();
  });

  it.each([
    ["checking", { status: "checking" }],
    ["paused", { status: "unavailable", reason: "paused" }],
    ["off", { status: "unavailable", reason: "off" }],
    ["budget used up", { status: "degraded", reason: "budget" }],
    ["unreachable", { status: "degraded", reason: "unreachable" }],
  ] as const)("renders nothing while AI is %s", (_name, state) => {
    renderControl(stub(explainAnswer), { availability: { ...ENABLED, state } });
    expect(screen.queryByRole("button")).toBeNull();
  });

  it.each([
    ["an entity that did not opt in", "planned-order", PO],
    ["an id that is not a plain token", "purchase-order", "a/b"],
  ])("renders nothing for %s", (_name, entity, id) => {
    renderControl(stub(explainAnswer), { entity, id });
    expect(screen.queryByRole("button")).toBeNull();
  });

  it("is absent from a page header that does not opt in, as on a list page", () => {
    render(
      <MemoryRouter>
        <PrincipalProvider duties={[COPILOT]}>
          <AvailabilityProvider value={ENABLED}>
            <PageHeader title="Purchase orders" />
          </AvailabilityProvider>
        </PrincipalProvider>
      </MemoryRouter>,
    );
    expect(screen.queryByRole("button", { name: copy.recordAssist.button })).toBeNull();
  });

  it("is shown by a page header that opts in with one prop", async () => {
    render(
      <MemoryRouter>
        <PrincipalProvider duties={[COPILOT]}>
          <AvailabilityProvider value={ENABLED}>
            <PageHeader title="Purchase order PO-1042" askAbout={{ entity: "purchase-order", id: PO }} />
          </AvailabilityProvider>
        </PrincipalProvider>
      </MemoryRouter>,
    );
    expect(await screen.findByRole("button", { name: copy.recordAssist.button })).toBeInTheDocument();
  });
});

describe("AskAboutRecord actions", () => {
  it("explains: sends the record reference in explain mode, labels the answer as AI, lists tool calls and links a citation the user may open", async () => {
    const ask = stub(explainAnswer);
    renderControl(ask, { duties: [COPILOT, "procurement.purchase-order.inquire"] });
    const user = await openPanel();
    const dialog = screen.getByRole("dialog", { name: "Ask about this purchase order" });
    await choose(user, copy.recordAssist.actions.explain.label);

    expect(ask).toHaveBeenCalledOnce();
    expect(ask.mock.calls[0]?.[0]).toEqual({ mode: "explain", message: copy.recordAssist.actions.explain.message, recordRef: `record:purchase-order/${PO}` });
    const answer = await within(dialog).findByRole("article", { name: "Copilot answer" });
    expect(within(answer).getByText(GENERATED_BADGE)).toBeInTheDocument();
    expect(within(answer).getByRole("link", { name: "Purchase order PO-1042" })).toHaveAttribute("href", "/purchasing/purchase-orders/PO-1042");
    expect(within(answer).getByRole("button", { name: "Why this?" })).toBeInTheDocument();
    expect(within(dialog).queryByRole("button", { name: "Insert draft" })).toBeNull();
    expect(within(dialog).queryByText(copy.recordAssist.missingTitle)).toBeNull();
  });

  it("names a citation as plain text when the user lacks the duty to open it", async () => {
    renderControl(stub(explainAnswer));
    const user = await openPanel();
    await choose(user, copy.recordAssist.actions.explain.label);
    await screen.findByRole("article", { name: "Copilot answer" });
    expect(screen.queryByRole("link", { name: "Purchase order PO-1042" })).toBeNull();
  });

  it("counts records the user cannot open and never names them", async () => {
    renderControl(stub({ ...explainAnswer, excluded: { reason: "NO_ACCESS", count: 2 } }));
    const user = await openPanel();
    await choose(user, copy.recordAssist.actions.explain.label);
    expect(await screen.findAllByText(/2 records were left out/)).not.toHaveLength(0);
  });

  it("shows what changed with only the audit trail needed, and states an empty history without a missing notice", async () => {
    const ask = stub({ answer: "There is no change history for this record.", citations: [], toolCalls: [{ tool: AUDIT_TRAIL_TOOL, status: "OK" }], refusal: null, generated: true });
    renderControl(ask);
    const user = await openPanel();
    await choose(user, copy.recordAssist.actions.changed.label);
    expect(await screen.findByText("There is no change history for this record.")).toBeInTheDocument();
    expect(ask.mock.calls[0]?.[0]).toMatchObject({ mode: "explain", message: copy.recordAssist.actions.changed.message });
    expect(screen.queryByText(copy.recordAssist.missingTitle)).toBeNull();
  });

  it("offers next steps as ordinary links filtered by the user's duties and posts nothing itself", async () => {
    renderControl(stub(explainAnswer), { duties: [COPILOT, "receiving.receipt.maintain"] });
    const user = await openPanel();
    await choose(user, copy.recordAssist.actions.next.label);
    const section = await screen.findByRole("region", { name: copy.recordAssist.nextHeading });
    expect(within(section).getByRole("link", { name: "Record arrival of the goods" })).toHaveAttribute("href", `/receiving/receipts/new?purchaseOrderId=${PO}`);
    expect(within(section).queryByRole("link", { name: "Post the vendor invoice" })).toBeNull();
    expect(within(section).queryByRole("button")).toBeNull();
  });

  it("shows no next-step links on the other two actions", async () => {
    renderControl(stub(explainAnswer), { duties: [COPILOT, "receiving.receipt.maintain"] });
    const user = await openPanel();
    await choose(user, copy.recordAssist.actions.explain.label);
    await screen.findByRole("article", { name: "Copilot answer" });
    expect(screen.queryByRole("region", { name: copy.recordAssist.nextHeading })).toBeNull();
  });
});

describe("AskAboutRecord missing parts and failures", () => {
  it("says which part could not be read when a tool call is missing or failed", async () => {
    const ask = stub({
      ...explainAnswer,
      toolCalls: [
        { tool: DOCUMENT_FLOW_TOOL, status: "OK" },
        { tool: WORKFLOW_HISTORY_TOOL, status: "DENIED", code: "AGT-0203" },
      ],
    });
    renderControl(ask);
    const user = await openPanel();
    await choose(user, copy.recordAssist.actions.explain.label);
    expect(await screen.findByText(copy.recordAssist.missing[AUDIT_TRAIL_TOOL]!)).toBeInTheDocument();
    expect(screen.getByText(copy.recordAssist.missing[WORKFLOW_HISTORY_TOOL]!)).toBeInTheDocument();
    expect(screen.queryByText(copy.recordAssist.missing[DOCUMENT_FLOW_TOOL]!)).toBeNull();
  });

  it("shows the refusal and every missing part when the copilot is refused the tools", async () => {
    renderControl(stub({ answer: "", citations: [], toolCalls: [], refusal: { code: "AGT-0203", message: "That tool is not allowed for you." }, generated: true }));
    const user = await openPanel();
    await choose(user, copy.recordAssist.actions.explain.label);
    expect(await screen.findByRole("alert")).toHaveTextContent("AGT-0203");
    for (const tool of [AUDIT_TRAIL_TOOL, DOCUMENT_FLOW_TOOL, WORKFLOW_HISTORY_TOOL]) {
      expect(screen.getByText(copy.recordAssist.missing[tool]!)).toBeInTheDocument();
    }
    expect(screen.queryByRole("region", { name: copy.recordAssist.nextHeading })).toBeNull();
  });

  it("notes that an answer at the tool-call limit may be partial", async () => {
    renderControl(stub({ ...explainAnswer, answer: "Here is what I found so far.", citations: [], toolCalls: Array.from({ length: 8 }, () => ({ tool: AUDIT_TRAIL_TOOL, status: "OK" as const })) }));
    const user = await openPanel();
    await choose(user, copy.recordAssist.actions.explain.label);
    expect(await screen.findByText(/may be partial/)).toBeInTheDocument();
    expect(screen.getByText(copy.recordAssist.missing[DOCUMENT_FLOW_TOOL]!)).toBeInTheDocument();
  });

  it("shows a danger alert with Try again on a network failure and leaves the page controls alone", async () => {
    const ask = vi.fn<AskFn>().mockRejectedValueOnce(new Error("The copilot could not answer. Try again.")).mockResolvedValueOnce(explainAnswer);
    renderControl(ask);
    const user = await openPanel();
    await choose(user, copy.recordAssist.actions.explain.label);
    expect(await screen.findByRole("alert")).toHaveTextContent("The copilot could not answer. Try again.");
    expect(screen.queryByText(copy.recordAssist.missingTitle)).toBeNull();
    await user.click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("article", { name: "Copilot answer" })).toBeInTheDocument();
    expect(ask).toHaveBeenCalledTimes(2);
  });

  it("reports an out-of-budget refusal so the button disappears", async () => {
    const reportRefusal = vi.fn();
    renderControl(stub({ answer: "", citations: [], toolCalls: [], refusal: { code: "AGT-0202", message: "Budget used." }, generated: true }), {
      availability: { ...ENABLED, reportRefusal },
    });
    const user = await openPanel();
    await choose(user, copy.recordAssist.actions.explain.label);
    await screen.findByRole("alert");
    expect(reportRefusal).toHaveBeenCalledWith("AGT-0202");
  });
});

describe("AskAboutRecord dialog", () => {
  it.each([
    ["Escape", async (user: ReturnType<typeof userEvent.setup>) => user.keyboard("{Escape}")],
    ["the Close button", async (user: ReturnType<typeof userEvent.setup>) => user.click(screen.getByRole("button", { name: copy.recordAssist.close }))],
  ])("closes with %s and returns focus to the button that opened it", async (_name, close) => {
    renderControl(stub(explainAnswer));
    const user = await openPanel();
    expect(screen.getByRole("dialog")).toBeInTheDocument();
    await close(user);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.getByRole("button", { name: copy.recordAssist.button })).toHaveFocus();
  });

  it("moves focus into the dialog and discards the answer when it closes", async () => {
    renderControl(stub(explainAnswer));
    const user = await openPanel();
    expect(screen.getByRole("dialog")).toContainElement(document.activeElement as HTMLElement);
    await choose(user, copy.recordAssist.actions.explain.label);
    await screen.findByRole("article", { name: "Copilot answer" });
    await user.keyboard("{Escape}");
    await user.click(screen.getByRole("button", { name: copy.recordAssist.button }));
    expect(screen.queryByRole("article", { name: "Copilot answer" })).toBeNull();
  });

  it("passes the accessibility check with an answer, a missing notice and next steps shown", async () => {
    renderControl(stub({ ...explainAnswer, toolCalls: [{ tool: AUDIT_TRAIL_TOOL, status: "OK" }] }), { duties: [COPILOT, "receiving.receipt.maintain"] });
    const user = await openPanel();
    await choose(user, copy.recordAssist.actions.next.label);
    await screen.findByRole("region", { name: copy.recordAssist.nextHeading });
    expect(pageViolations()).toEqual([]);
  });

  it("keeps assistant content out of browser storage and history", () => {
    expect(source).not.toMatch(/localStorage|sessionStorage|indexedDB|history\.(push|replace)State|document\.title/);
  });
});
