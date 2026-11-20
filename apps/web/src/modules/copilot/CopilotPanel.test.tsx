import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { CopilotError, type ChatResponse } from "./api/client";
import { AvailabilityProvider, type AiAvailability } from "../../shell/assistant/AvailabilityProvider";
import { pageViolations } from "./a11yTest";
import { CopilotPanel } from "./CopilotPanel";
import { canInsertDraft, GENERATED_BADGE, isCopilotMode, toolCallLabel } from "./domain";

const CONVERSATION_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11";
const newConversation = async () => CONVERSATION_ID;

const answer: ChatResponse = {
  answer: "Revenue was 1,200.00 USD in October, against 1,050.00 in September.",
  citations: [{ sourceType: "METRIC", sourceRef: "revenue", label: "Revenue, 2026-10 vs 2026-09" }],
  toolCalls: [{ tool: "depot.ledger.journal.get", status: "OK" }],
  refusal: null,
  generated: true,
};

describe("copilot domain rules", () => {
  it("inserts drafts only in draft mode and never after a refusal", () => {
    expect(canInsertDraft("draft", false)).toBe(true);
    expect(canInsertDraft("draft", true)).toBe(false);
    expect(canInsertDraft("ask", false)).toBe(false);
  });

  it("recognises the four modes and labels tool calls with their code", () => {
    expect(isCopilotMode("explain")).toBe(true);
    expect(isCopilotMode("delete")).toBe(false);
    expect(toolCallLabel({ tool: "depot.ap.invoice.post", status: "DENIED", code: "AGT-0203" })).toBe(
      "depot.ap.invoice.post: DENIED (AGT-0203)",
    );
  });
});

describe("CopilotPanel", () => {
  it("sends the mode and message, then shows a labelled answer with tool calls and citations", async () => {
    const ask = vi.fn(async () => answer);
    render(<CopilotPanel createConversation={newConversation} ask={ask} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Ask Copilot"), "what was revenue last month vs the month before");
    await user.click(screen.getByRole("button", { name: "Send" }));

    expect(ask).toHaveBeenCalledWith({ mode: "ask", message: "what was revenue last month vs the month before" }, expect.any(Function), expect.any(Function), undefined, CONVERSATION_ID);
    expect(await screen.findByRole("article", { name: "Copilot answer" })).toHaveTextContent(answer.answer);
    expect(screen.getByText(GENERATED_BADGE)).toBeInTheDocument();
    await user.click(screen.getByRole("button", { name: "Data used" }));
    expect(screen.getByRole("list", { name: "Tools called" })).toHaveTextContent("depot.ledger.journal.get: OK");
    expect(screen.getByRole("list", { name: "Sources" })).toHaveTextContent("Revenue, 2026-10 vs 2026-09");
    expect(screen.queryByText("No answer yet")).toBeNull();
    expect(pageViolations()).toEqual([]);
  });

  it("sends the initial message once in ask mode and shows the answer", async () => {
    const ask = vi.fn(async () => answer);
    render(<CopilotPanel createConversation={newConversation} ask={ask} initialMessage="  how is cash  " />);
    expect(await screen.findByRole("article", { name: "Copilot answer" })).toHaveTextContent(answer.answer);
    expect(ask).toHaveBeenCalledTimes(1);
    expect(ask).toHaveBeenCalledWith({ mode: "ask", message: "how is cash" }, expect.any(Function), expect.any(Function), undefined, CONVERSATION_ID);
  });

  it("sends nothing without an initial message and shows an alert when the initial message fails", async () => {
    const quiet = vi.fn(async () => answer);
    const { unmount } = render(<CopilotPanel createConversation={newConversation} ask={quiet} initialMessage="   " />);
    expect(quiet).not.toHaveBeenCalled();
    unmount();
    const failing = vi.fn(async () => Promise.reject(new Error("Copilot is down.")));
    render(<CopilotPanel createConversation={newConversation} ask={failing} initialMessage="hello" />);
    expect(await screen.findByRole("alert")).toHaveTextContent("Copilot is down.");
    expect(failing).toHaveBeenCalledTimes(1);
  });

  it("switches mode with pressed state and sends the record context", async () => {
    const ask = vi.fn(async () => answer);
    render(<CopilotPanel createConversation={newConversation} ask={ask} recordRef="ledger.journal:JV-0042" />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Explain this record" }));
    expect(screen.getByRole("button", { name: "Explain this record" })).toHaveAttribute("aria-pressed", "true");
    expect(screen.getByRole("button", { name: "Ask the data" })).toHaveAttribute("aria-pressed", "false");
    await user.type(screen.getByLabelText("Ask Copilot"), "why is this journal reversed");
    await user.click(screen.getByRole("button", { name: "Send" }));
    expect(ask).toHaveBeenCalledWith({ mode: "explain", message: "why is this journal reversed", recordRef: "ledger.journal:JV-0042" }, expect.any(Function), expect.any(Function), undefined, CONVERSATION_ID);
  });

  it("inserts a draft only when the user clicks Insert draft", async () => {
    const onInsertDraft = vi.fn();
    render(<CopilotPanel createConversation={newConversation} ask={async () => ({ ...answer, answer: "Dear customer, your invoice is overdue." })} onInsertDraft={onInsertDraft} initialMode="draft" />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Ask Copilot"), "dunning letter for ACME");
    await user.click(screen.getByRole("button", { name: "Send" }));
    await screen.findByRole("article", { name: "Copilot answer" });
    expect(onInsertDraft).not.toHaveBeenCalled();
    await user.click(screen.getByRole("button", { name: "Insert draft" }));
    expect(onInsertDraft).toHaveBeenCalledWith("Dear customer, your invoice is overdue.");
  });

  it("shows a refusal with its code and offers no draft insert", async () => {
    const refusal: ChatResponse = {
      ...answer,
      answer: "",
      citations: [],
      toolCalls: [{ tool: "depot.ap.invoice.post", status: "DENIED", code: "AGT-0203" }],
      refusal: { code: "AGT-0203", message: "I cannot do that: you do not hold duty APInvoicePost required by depot.ap.invoice.post." },
    };
    render(<CopilotPanel createConversation={newConversation} ask={async () => refusal} onInsertDraft={vi.fn()} initialMode="draft" />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Ask Copilot"), "post the invoice");
    await user.click(screen.getByRole("button", { name: "Send" }));
    expect(await screen.findByText(/do not hold duty APInvoicePost/)).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Insert draft" })).toBeNull();
  });

  it("reports an unavailable copilot as an alert", async () => {
    render(<CopilotPanel createConversation={newConversation} ask={async () => { throw new Error("Copilot is unavailable (HTTP 503)."); }} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Ask Copilot"), "hello");
    await user.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("HTTP 503"));
  });

  it("shows the answer as it streams, labelled, then the final answer", async () => {
    let finish: (r: ChatResponse) => void = () => undefined;
    const ask = vi.fn(
      (_req: unknown, onToken?: (text: string) => void) =>
        new Promise<ChatResponse>((resolve) => {
          onToken?.("Revenue was ");
          onToken?.("1,200.00 USD");
          finish = resolve;
        }),
    );
    render(<CopilotPanel createConversation={newConversation} ask={ask} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Ask Copilot"), "revenue last month");
    await user.click(screen.getByRole("button", { name: "Send" }));

    const live = await screen.findByRole("article", { name: "Copilot answer in progress" });
    expect(live).toHaveTextContent("Revenue was 1,200.00 USD");
    expect(live).toHaveTextContent(GENERATED_BADGE);
    expect(live).toHaveAttribute("aria-busy", "true");
    expect(screen.queryByText("No answer yet")).toBeNull();
    expect(pageViolations()).toEqual([]);

    finish(answer);
    expect(await screen.findByRole("article", { name: "Copilot answer" })).toHaveTextContent(answer.answer);
    expect(screen.queryByRole("article", { name: "Copilot answer in progress" })).toBeNull();
  });

  it("replaces the partial text with the safe message when the answer is withdrawn, and keeps only that message", async () => {
    const safe = "This answer could not be shown. Try again or ask your administrator.";
    let finish: (r: ChatResponse) => void = () => undefined;
    const ask = vi.fn(
      (_req: unknown, onToken?: (text: string) => void, onWithdrawn?: (message: string, code: string) => void) =>
        new Promise<ChatResponse>((resolve) => {
          onToken?.("The salary is 85000. ");
          onWithdrawn?.(safe, "AGT-0220");
          finish = resolve;
        }),
    );
    render(<CopilotPanel createConversation={newConversation} ask={ask} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Ask Copilot"), "pay");
    await user.click(screen.getByRole("button", { name: "Send" }));
    const live = await screen.findByRole("article", { name: "Copilot answer in progress" });
    expect(live).toHaveTextContent(safe);
    expect(live).not.toHaveTextContent("85000");
    expect(live).toHaveTextContent("AGT-0220");

    finish({ answer: safe, citations: [], toolCalls: [], refusal: { code: "AGT-0220", message: safe }, generated: true });
    const final = await screen.findByRole("article", { name: "Copilot answer" });
    expect(final).toHaveTextContent(safe);
    expect(final).not.toHaveTextContent("85000");
  });

  it("drops a partial answer when the stream fails, and shows the error", async () => {
    const ask = vi.fn(async (_req: unknown, onToken?: (text: string) => void) => {
      onToken?.("half an ans");
      throw new Error("AGT-0206: Provider unavailable.");
    });
    render(<CopilotPanel createConversation={newConversation} ask={ask} />);
    const user = userEvent.setup();
    await user.type(screen.getByLabelText("Ask Copilot"), "hello");
    await user.click(screen.getByRole("button", { name: "Send" }));
    await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("AGT-0206"));
    expect(screen.queryByText("half an ans")).toBeNull();
  });

  it("does not send an empty message", async () => {
    const ask = vi.fn(async () => answer);
    render(<CopilotPanel createConversation={newConversation} ask={ask} />);
    const user = userEvent.setup();
    await user.click(screen.getByRole("button", { name: "Send" }));
    expect(ask).not.toHaveBeenCalled();
  });

  describe("when the assistant is stopped or unreachable", () => {
    const plain = "The assistant has reached its usage limit, so it can't answer right now. Every page and workflow still works.";

    function renderWith(ask: () => Promise<ChatResponse>) {
      const availability: AiAvailability = {
        state: { status: "enabled", budget: "OK" },
        reportRefusal: vi.fn(),
        reportFailure: vi.fn(),
      };
      render(
        <AvailabilityProvider value={availability}>
          <CopilotPanel createConversation={newConversation} ask={ask} />
        </AvailabilityProvider>,
      );
      return availability;
    }

    async function submit() {
      const user = userEvent.setup();
      await user.type(screen.getByLabelText("Ask Copilot"), "how is cash");
      await user.click(screen.getByRole("button", { name: "Send" }));
    }

    it("shows the code and a plain message for a budget refusal in the answer, reports it, and asks only once", async () => {
      const ask = vi.fn(async () => ({ ...answer, answer: "", refusal: { code: "AGT-0202", message: "budget exhausted" } }));
      const availability = renderWith(ask);
      await submit();
      const alert = within(await screen.findByRole("article", { name: "Copilot answer" })).getByRole("alert");
      expect(alert).toHaveTextContent("AGT-0202");
      expect(alert).toHaveTextContent(plain);
      expect(availability.reportRefusal).toHaveBeenCalledWith("AGT-0202");
      expect(ask).toHaveBeenCalledTimes(1);
    });

    it("treats a budget refusal that arrives as an error response the same way", async () => {
      const ask = vi.fn(async () => Promise.reject(new CopilotError("Copilot is unavailable (HTTP 422).", "AGT-0202")));
      const availability = renderWith(ask);
      await submit();
      const alert = within(await screen.findByRole("article", { name: "Copilot answer" })).getByRole("alert");
      expect(alert).toHaveTextContent(plain);
      expect(alert).toHaveTextContent("AGT-0202");
      expect(availability.reportRefusal).toHaveBeenCalledWith("AGT-0202");
      expect(availability.reportFailure).not.toHaveBeenCalled();
      expect(ask).toHaveBeenCalledTimes(1);
    });

    it("shows an inline error and no dialog for a server error or a timeout, without retrying", async () => {
      const ask = vi.fn(async () => Promise.reject(new CopilotError("Copilot is unavailable (HTTP 503).")));
      const availability = renderWith(ask);
      await submit();
      await waitFor(() => expect(screen.getByRole("alert")).toHaveTextContent("Copilot is unavailable (HTTP 503)."));
      expect(screen.queryByRole("dialog")).toBeNull();
      expect(availability.reportFailure).toHaveBeenCalledTimes(1);
      expect(availability.reportRefusal).not.toHaveBeenCalled();
      expect(ask).toHaveBeenCalledTimes(1);
    });
  });
});
