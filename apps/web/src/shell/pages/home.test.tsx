import { cleanup, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { clearHandedOffPrompt, handOffPrompt } from "../assistant/session";
import { CopilotHome } from "../../modules/copilot/pages";
import { loadModules, type DepotModule } from "../../modules/registry";
import { Shell } from "../Shell";
import { AVAILABILITY_PATH } from "../assistant/availability";
import { availableAssistant, json, pageViolations, server, useFakeServer, type Reply } from "../testing/fakeServer";
import { completedOnboardingStorage } from "../testing/onboardingFixtures";
import { AllFeaturesPage } from "./AllFeaturesPage";
import { HomePage } from "./HomePage";
import { workspaceKpis } from "./kpi";

useFakeServer();

const MAX_HOME_LINKS = 30;
const PAGE_CONTROLS = ":is(a[href], button, input, select, textarea):not([class*=\"topbar\"] *)";

const modules: DepotModule[] = [
  {
    id: "ledger",
    title: "General ledger",
    routes: [],
    nav: [
      { label: "Journal batches", to: "/ledger/journal-batches" },
      { label: "Open items", to: "/ledger/open-items" },
    ],
  },
];

const recordModules: DepotModule[] = [{ id: "ledger", title: "General ledger", routes: [{ path: "/ledger/entries/:id", element: <div>Entry</div> }], nav: [] }];

const item = (n: number) => ({
  id: `00000000-0000-4000-8000-00000000000${n}`,
  source: "purchasing",
  kind: "Requisition approval",
  reason: "Waiting for approval",
  number: `PR-${n}`,
  link: `/purchasing/requisitions/${n}`,
  actions: ["open"],
  createdAt: "2026-11-01T00:00:00Z",
});

const enabled = availableAssistant;
let availability: Reply = json(200, enabled);
beforeEach(() => {
  availability = json(200, enabled);
});

function renderHome(duties: string[], shellModules: DepotModule[] = modules, entry: { pathname: string } = { pathname: "/" }, features?: Record<string, boolean>) {
  const inner = server.handler;
  server.handler = (url, init) => (url.pathname === AVAILABILITY_PATH ? availability : inner(url, init));
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route element={<Shell modules={shellModules} duties={duties} features={features} />}>
          <Route path="/" element={<HomePage />} />
          <Route path="/copilot" element={<CopilotHome />} />
          <Route path="/features" element={<AllFeaturesPage />} />
          <Route path="/ledger/entries/:id" element={<div>Entry</div>} />
        </Route>
      </Routes>
    </MemoryRouter>,
  );
}

const main = () => within(screen.getByRole("main"));

describe("role home", () => {
  it("shows the My work count and the top five items with a link to all of them", async () => {
    server.handler = () => json(200, { items: [1, 2, 3, 4, 5, 6, 7].map(item), unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    expect(await main().findByText("7 items are waiting for you.")).toBeInTheDocument();
    const list = main().getByRole("list", { name: "My work" });
    expect(within(list).getAllByRole("link")).toHaveLength(5);
    expect(main().getByRole("link", { name: "See all my work" })).toHaveAttribute("href", "/workflow/inbox");
  });

  it("shows a plus when more work is waiting than was read", async () => {
    server.handler = () => json(200, { items: [item(1), item(2)], unavailable: [], truncated: ["ledger"] });
    renderHome(["inbox.read"]);
    expect(await main().findByText("2+ items are waiting for you.")).toBeInTheDocument();
  });

  it("marks the active workspace and offers at most five shortcuts in it", () => {
    renderHome([]);
    expect(main().getByText("Current workspace")).toBeInTheDocument();
    expect(main().getAllByRole("list", { name: /Top tasks/ }).every((l) => within(l).getAllByRole("link").length <= 5)).toBe(true);
  });

  it("says You're all caught up when nothing is waiting, and has no axe violations", async () => {
    server.handler = () => json(200, { items: [], unavailable: [], truncated: [] });
    const { container } = renderHome(["inbox.read"]);
    expect(await main().findByText("You're all caught up")).toBeInTheDocument();
    expect(pageViolations(container)).toEqual([]);
  });

  it("explains a partial load and retries", async () => {
    const user = userEvent.setup();
    server.handler = () => json(200, { items: [item(1)], unavailable: [{ source: "ledger", reason: "timeout" }], truncated: [] });
    renderHome(["inbox.read"]);
    expect(await main().findByText("Some items could not be loaded: journal batches")).toBeInTheDocument();
    await user.click(main().getByRole("button", { name: "Try again" }));
    await waitFor(() => expect(server.calls.filter((c) => c.url.pathname === "/api/v1/inbox")).toHaveLength(2));
  });

  it("shows an error with retry when My work cannot be loaded", async () => {
    server.handler = () => json(500, { code: "COM-0500", detail: "boom" });
    renderHome(["inbox.read"]);
    expect(await main().findByRole("button", { name: "Try again" })).toBeInTheDocument();
  });

  it("leaves My work out, and asks nothing of the server, for users without the inbox duty", () => {
    renderHome([]);
    expect(main().queryByText("My work")).not.toBeInTheDocument();
    expect(server.calls).toHaveLength(0);
  });

  it("keeps the number of links on home within the limit even for a user with many workspaces", async () => {
    const all = loadModules();
    const kpiDuties = Object.values(workspaceKpis).flatMap((tiles) => tiles.map((t) => t.duty));
    const duties = [...new Set(all.flatMap((m) => m.nav.flatMap((n) => (n.duty ? [n.duty] : []))).concat("inbox.read", ...kpiDuties))];
    server.handler = () => json(200, { items: [1, 2, 3, 4, 5, 6, 7].map(item), unavailable: [], truncated: [] });
    renderHome(duties, all);
    await main().findByText("7 items are waiting for you.");
    // The current workspace's number tiles count toward the limit too.
    await waitFor(() => expect(main().getAllByRole("list", { name: / at a glance$/ }).length).toBeGreaterThan(1));
    expect(main().getAllByRole("link").length).toBeLessThanOrEqual(MAX_HOME_LINKS);
  });

  it("shows at-a-glance tiles: waiting, approvals, overdue and the oldest wait", async () => {
    const old = { ...item(1), actions: ["approve"], createdAt: "2026-01-01T00:00:00Z", dueDate: "2026-01-02" };
    const fresh = { ...item(2), actions: ["open"], createdAt: new Date().toISOString() };
    server.handler = () => json(200, { items: [old, fresh], unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    const tiles = within(await main().findByRole("list", { name: "My work at a glance" }));
    expect(tiles.getByText("Waiting for me").closest("li")).toHaveTextContent("2");
    expect(tiles.getByText("To approve").closest("li")).toHaveTextContent("1");
    expect(tiles.getByText("Overdue").closest("li")).toHaveTextContent("1");
    expect(Number(tiles.getByText("Oldest, in days").closest("li")?.textContent?.replace(/\D/g, ""))).toBeGreaterThan(100);
  });

  it("puts the count on a My work link in the sidebar, and no badge when nothing waits", async () => {
    server.handler = () => json(200, { items: [item(1), item(2), item(3)], unavailable: [], truncated: [] });
    const { unmount } = renderHome(["inbox.read"]);
    const link = await screen.findByRole("link", { name: /^My work.*3 waiting$/ });
    expect(link).toHaveAttribute("href", "/workflow/inbox");
    unmount();
    server.handler = () => json(200, { items: [], unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    await main().findByText("You're all caught up");
    expect(screen.getByRole("link", { name: "My work" })).toBeInTheDocument();
  });

  it("reads the inbox once for the sidebar, the tiles and the list", async () => {
    server.handler = () => json(200, { items: [item(1)], unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    await main().findByText("1 item is waiting for you.");
    expect(server.calls.filter((c) => c.url.pathname === "/api/v1/inbox")).toHaveLength(1);
  });

  it("tells a new user what will appear in My work and offers no My work link without the duty", async () => {
    server.handler = () => json(200, { items: [], unavailable: [], truncated: [] });
    renderHome(["inbox.read"]);
    expect(await main().findByText(/Approvals, holds and other tasks assigned to you will appear here/)).toBeInTheDocument();
    cleanup();
    renderHome([]);
    expect(screen.queryByRole("link", { name: /^My work/ })).not.toBeInTheDocument();
  });

  it("offers a switch button, not more links, for the other workspaces", async () => {
    const user = userEvent.setup();
    renderHome([], [
      ...modules,
      { id: "purchasing", title: "Purchasing", routes: [], nav: [{ label: "Purchase orders", to: "/purchasing/purchase-orders" }] },
    ]);
    const switchers = main().getAllByRole("button", { name: /^Switch to / });
    expect(switchers.length).toBeGreaterThan(0);
    const before = main().getAllByRole("heading", { level: 3 })[0].textContent;
    await user.click(switchers[0]);
    expect(main().getAllByRole("heading", { level: 3 })[0].textContent).not.toBe(before);
  });

  it("remembers the pages opened last and lists them on home", async () => {
    const user = userEvent.setup();
    render(
      <MemoryRouter initialEntries={["/ledger/journal-batches"]}>
        <Routes>
          <Route element={<Shell modules={modules} duties={[]} identity="u1" onboardingStorage={completedOnboardingStorage("u1")} />}>
            <Route path="/" element={<HomePage />} />
            <Route path="/ledger/journal-batches" element={<p>Journals page</p>} />
          </Route>
        </Routes>
      </MemoryRouter>,
    );
    await user.click(within(screen.getByRole("navigation", { name: "Workspaces" })).getByRole("link", { name: "Home" }));
    const recent = within(main().getByRole("list", { name: "Recent" }));
    expect(recent.getAllByRole("link")[0]).toHaveAttribute("href", "/ledger/journal-batches");
  });

  it("invites an administrator to finish setup, and stays quiet when setup is finished", async () => {
    const setup = (finished: number) => json(200, { steps: [], total: 10, finished, complete: finished === 10 });
    server.handler = (url) => (url.pathname === "/api/v1/tenant/setup" ? setup(4) : undefined);
    const first = renderHome(["identity.setup.maintain"]);
    expect(await main().findByText("4 of 10 setup steps are finished.")).toBeInTheDocument();
    expect(main().getByRole("link", { name: "Continue setup" })).toHaveAttribute("href", "/setup");
    first.unmount();
    server.handler = (url) => (url.pathname === "/api/v1/tenant/setup" ? setup(10) : undefined);
    renderHome(["identity.setup.maintain"]);
    await waitFor(() => expect(server.calls.some((c) => c.url.pathname === "/api/v1/tenant/setup")).toBe(true));
    expect(main().queryByText(/setup steps are finished/)).not.toBeInTheDocument();
  });

  describe("assistant-led home", () => {
    const COPILOT_DUTY = "mcp.copilot.use";
    const askBox = () => main().findByRole("textbox", { name: "Ask or do anything..." });

    it("puts the prompt first in the main landmark and the workspace cards in a Browse workspaces region", async () => {
      renderHome([COPILOT_DUTY]);
      const box = await askBox();
      expect(screen.getByRole("main").querySelector(PAGE_CONTROLS)).toBe(box);
      const region = within(main().getByRole("region", { name: "Browse workspaces" }));
      expect(region.getByText("Current workspace")).toBeInTheDocument();
      expect(region.getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
      expect(box.compareDocumentPosition(main().getByRole("region", { name: "Browse workspaces" })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it("shows an error and makes no request when the prompt is empty", async () => {
      const user = userEvent.setup();
      renderHome([COPILOT_DUTY]);
      await askBox();
      await user.click(main().getByRole("button", { name: "Send" }));
      expect(main().getByRole("alert")).toHaveTextContent("Type a question or a request first.");
      expect(server.calls.filter((c) => c.url.pathname !== AVAILABILITY_PATH)).toHaveLength(0);
    });

    const CONVERSATIONS = "/api/v1/agents/copilot/conversations";
    const CONVERSATION_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11";
    const MESSAGES = `${CONVERSATIONS}/${CONVERSATION_ID}/messages`;
    const chat = (answer = json(200, { answer: "Cash is steady.", citations: [], toolCalls: [], refusal: null, generated: true })) => (url: URL) =>
      url.pathname === "/api/v1/copilot/chat" ? routeReply : url.pathname === CONVERSATIONS ? json(201, { conversationId: CONVERSATION_ID }) : url.pathname === MESSAGES ? answer : undefined;
    let routeReply: Reply = json(200, { kind: "ask", confidence: 1, generated: true });
    beforeEach(() => {
      routeReply = json(200, { kind: "ask", confidence: 1, generated: true });
    });
    const chats = () => server.calls.filter((c) => c.url.pathname === "/api/v1/copilot/chat" || c.url.pathname === MESSAGES);

    it("classifies the typed text first, then opens the copilot in ask mode and sends it once", async () => {
      const user = userEvent.setup();
      server.handler = chat();
      renderHome([COPILOT_DUTY]);
      await user.type(await askBox(), "how is cash{Enter}");
      expect(await main().findByRole("article", { name: "Copilot answer" })).toHaveTextContent("Cash is steady.");
      expect(chats().map((c) => c.body)).toEqual([
        { mode: "route", message: "how is cash" },
        { mode: "ask", message: "how is cash" },
      ]);
    });

    it.each(["navigate", "act"])("opens the copilot ask path when the %s handler is not registered yet", async (kind) => {
      const user = userEvent.setup();
      routeReply = json(200, { kind, confidence: 0.9, generated: true });
      server.handler = chat();
      renderHome([COPILOT_DUTY, "agents.invoke"]);
      await user.type(await askBox(), "show me overdue invoices{Enter}");
      expect(await main().findByRole("article", { name: "Copilot answer" })).toBeInTheDocument();
      expect(chats().map((c) => (c.body as { mode: string }).mode)).toEqual(["route", "ask"]);
    });

    it("falls back to the features page, never to the copilot, when the classification fails", async () => {
      const user = userEvent.setup();
      routeReply = json(503, { code: "AGT-0500" });
      server.handler = chat();
      renderHome([COPILOT_DUTY]);
      await user.type(await askBox(), "apply this payment to invoice INV-1042{Enter}");
      expect(await screen.findByRole("searchbox", { name: "Filter features" })).toHaveValue("apply this payment to invoice INV-1042");
      expect(chats().map((c) => (c.body as { mode: string }).mode)).toEqual(["route"]);
    });

    it("sends no copilot request and goes to the features page when the assistant turns unavailable after render", async () => {
      const user = userEvent.setup();
      server.handler = chat();
      renderHome([COPILOT_DUTY]);
      const box = await askBox();
      availability = json(200, { ...enabled, killSwitch: { ...enabled.killSwitch, halted: true } });
      fireEvent.focus(window);
      await user.type(box, "show me overdue invoices{Enter}");
      await waitFor(() => expect(screen.queryByRole("textbox", { name: "Ask or do anything..." })).not.toBeInTheDocument());
      expect(chats()).toHaveLength(0);
    });

    it("answers inline on the home, in one new conversation, without opening the copilot page", async () => {
      const user = userEvent.setup();
      server.handler = chat(json(200, { answer: "Cash is steady.", citations: [], toolCalls: [{ tool: "metric.search", status: "OK" }], refusal: null, generated: true }));
      renderHome([COPILOT_DUTY]);
      await user.type(await askBox(), "how is cash{Enter}");
      const answer = await main().findByRole("article", { name: "Copilot answer" });
      expect(within(answer).getByText("AI-generated: check before you use it")).toBeInTheDocument();
      expect(screen.getByRole("textbox", { name: "Ask or do anything..." })).toBeInTheDocument();
      expect(server.calls.filter((c) => c.url.pathname === CONVERSATIONS)).toHaveLength(1);
      expect(chats().at(-1)?.url.pathname).toBe(MESSAGES);
      expect(screen.getAllByRole("status").some((r) => r.textContent === "The copilot has finished answering.")).toBe(true);
    });

    it.each([
      ["an empty string", ""],
      ["only whitespace", "   "],
      ["nothing handed off", undefined],
    ])("sends nothing when the copilot opens with %s as its prompt", async (_name, handedOff) => {
      clearHandedOffPrompt();
      if (handedOff !== undefined) handOffPrompt(handedOff);
      renderHome([COPILOT_DUTY], modules, { pathname: "/copilot" });
      await screen.findByRole("heading", { name: "Copilot" });
      expect(chats()).toHaveLength(0);
    });

    it("answers a question asked from the command bar while the copilot page is already open", async () => {
      const user = userEvent.setup();
      clearHandedOffPrompt();
      server.handler = chat();
      renderHome([COPILOT_DUTY], modules, { pathname: "/copilot" });
      await screen.findByRole("heading", { name: "Copilot" });
      expect(chats()).toHaveLength(0);
      await user.keyboard("{Control>}k{/Control}");
      await user.type(await screen.findByLabelText("Find a page"), "zzqq");
      await screen.findByRole("group", { name: "Assistant" });
      await user.keyboard("{Enter}");
      await waitFor(() => expect(chats().map((c) => c.body)).toContainEqual({ mode: "ask", message: "zzqq" }));
    });

    it("sends each new command-bar prompt once while the copilot page stays open", async () => {
      const user = userEvent.setup();
      clearHandedOffPrompt();
      server.handler = chat();
      renderHome([COPILOT_DUTY], modules, { pathname: "/copilot" });
      await screen.findByRole("heading", { name: "Copilot" });
      for (const text of ["zzqq", "yyww"]) {
        await user.keyboard("{Control>}k{/Control}");
        await user.type(await screen.findByLabelText("Find a page"), text);
        await screen.findByRole("group", { name: "Assistant" });
        await user.keyboard("{Enter}");
        await waitFor(() => expect(chats().map((c) => c.body)).toContainEqual({ mode: "ask", message: text }));
      }
      await user.type(screen.getByLabelText("Ask Copilot"), "more");
      const sent = chats().map((c) => c.body as { mode: string; message: string });
      expect(sent.filter((b) => b.mode === "ask" && (b.message === "zzqq" || b.message === "yyww"))).toHaveLength(2);
    });

    it("updates the record context when Ctrl+/ is pressed on a record page and again on the copilot page", async () => {
      const user = userEvent.setup();
      clearHandedOffPrompt();
      server.handler = chat();
      renderHome([COPILOT_DUTY], recordModules, { pathname: "/ledger/entries/JE-77" });
      await user.keyboard("{Control>}/{/Control}");
      await user.type(await screen.findByLabelText("Ask Copilot"), "first");
      await user.click(screen.getByRole("button", { name: "Send" }));
      await waitFor(() => expect(chats().map((c) => c.body)).toContainEqual({ mode: "ask", message: "first", recordRef: "record:entries/JE-77" }));
      await user.keyboard("{Control>}/{/Control}");
      await user.type(await screen.findByLabelText("Ask Copilot"), "second");
      await user.click(screen.getByRole("button", { name: "Send" }));
      await waitFor(() => expect(chats().map((c) => c.body)).toContainEqual({ mode: "ask", message: "second" }));
    });

    it("sends nothing when the handed-off prompt is older than ten seconds", async () => {
      handOffPrompt("how is cash");
      const now = Date.now();
      vi.spyOn(Date, "now").mockReturnValue(now + 11_000);
      renderHome([COPILOT_DUTY], modules, { pathname: "/copilot" });
      await screen.findByRole("heading", { name: "Copilot" });
      vi.restoreAllMocks();
      expect(chats()).toHaveLength(0);
    });

    it("passes an oversize prompt through so the copilot shows the refusal instead of dropping it", async () => {
      const prompt = "x".repeat(4500);
      server.handler = chat(json(200, { answer: "", citations: [], toolCalls: [], refusal: { code: "AGT-0001", message: "That question is too long." }, generated: true }));
      handOffPrompt(prompt);
      renderHome([COPILOT_DUTY], modules, { pathname: "/copilot" });
      expect(await main().findByText("That question is too long.")).toBeInTheDocument();
      expect(chats()[0].body).toEqual({ mode: "ask", message: prompt });
    });

    it("shows the classic cards as the primary content, with no prompt and no copilot request, without the duty", () => {
      renderHome([]);
      expect(screen.queryByRole("textbox", { name: "Ask or do anything..." })).not.toBeInTheDocument();
      expect(main().queryByRole("button", { name: "Send" })).not.toBeInTheDocument();
      expect(main().queryByRole("region", { name: "Browse workspaces" })).not.toBeInTheDocument();
      expect(main().getByText("Current workspace")).toBeInTheDocument();
      expect(main().getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
      expect(server.calls.filter((c) => c.url.pathname.includes("/copilot"))).toHaveLength(0);
    });

    it("shows the classic home, with no prompt to drop, when the copilot is switched off for the installation", () => {
      renderHome([COPILOT_DUTY], modules, { pathname: "/" }, { "install:node": false });
      expect(screen.queryByRole("textbox", { name: "Ask or do anything..." })).not.toBeInTheDocument();
      expect(main().queryByRole("button", { name: "Send" })).not.toBeInTheDocument();
      expect(main().getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
    });

    it("offers a workspace page as a link, one click away, inside the secondary region", async () => {
      renderHome([COPILOT_DUTY]);
      const region = within(await main().findByRole("region", { name: "Browse workspaces" }));
      const tasks = region.getAllByRole("list", { name: /Top tasks/ })[0];
      expect(within(tasks).getAllByRole("link")[0]).toHaveAttribute("href", expect.stringMatching(/^\//));
    });
  });

  describe("when the assistant is not available", () => {
    const COPILOT_DUTY = "mcp.copilot.use";
    const promptBox = () => screen.queryByRole("textbox", { name: "Ask or do anything..." });
    const paused = "The assistant is paused";
    const limit = "The assistant has reached its usage limit";
    const unreachable = "The assistant can't be reached right now";
    const withKill = (kill: object) => ({ ...enabled, killSwitch: { ...enabled.killSwitch, ...kill } });

    it.each([
      ["the kill switch is on", json(200, withKill({ halted: true })), paused],
      ["the budget is used up", json(200, { ...enabled, budgetStatus: "HARD" }), limit],
      ["the company has the assistant off", json(200, { ...enabled, copilotEnabled: false }), "The assistant is turned off"],
      ["the deployment waits for sign-off", json(200, { ...enabled, copilotEnabled: false, signoffPending: true }), "The assistant is not available in this deployment until sign-off"],
      ["the read fails with a server error", json(503, {}), unreachable],
      ["the body cannot be read", json(200, { killSwitch: { halted: "false" } }), unreachable],
    ])("shows the classic home, a notice and no prompt when %s", async (_name, reply: Reply, title) => {
      availability = reply;
      renderHome([COPILOT_DUTY]);
      expect(await main().findByText(title)).toBeInTheDocument();
      expect(promptBox()).not.toBeInTheDocument();
      expect(main().getByText("Current workspace")).toBeInTheDocument();
      expect(main().getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
    });

    it("keeps My work working and has no axe violations while the assistant is unreachable", async () => {
      availability = json(503, {});
      server.handler = (url) => (url.pathname === "/api/v1/inbox" ? json(200, { items: [item(1), item(2)], unavailable: [], truncated: [] }) : undefined);
      const { container } = renderHome([COPILOT_DUTY, "inbox.read"]);
      expect(await main().findByText("2 items are waiting for you.")).toBeInTheDocument();
      expect(await main().findByText(unreachable)).toBeInTheDocument();
      expect(screen.queryByRole("dialog")).not.toBeInTheDocument();
      expect(pageViolations(container)).toEqual([]);
    });

    it("returns the assistant-led home on the next check when the budget is raised, with no notice left", async () => {
      availability = json(200, { ...enabled, budgetStatus: "HARD" });
      renderHome([COPILOT_DUTY]);
      expect(await main().findByText(limit)).toBeInTheDocument();
      availability = json(200, enabled);
      fireEvent.focus(window);
      expect(await main().findByRole("textbox", { name: "Ask or do anything..." })).toBeInTheDocument();
      expect(main().queryByText(limit)).not.toBeInTheDocument();
    });

    it("shows a soft budget warning to an administrator and nothing to anyone else", async () => {
      availability = json(200, { ...enabled, budgetStatus: "SOFT" });
      const admin = renderHome([COPILOT_DUTY, "agents.admin"]);
      expect(await main().findByText("The assistant is close to its usage limit")).toBeInTheDocument();
      expect(promptBox()).toBeInTheDocument();
      admin.unmount();
      renderHome([COPILOT_DUTY]);
      expect(await main().findByRole("textbox", { name: "Ask or do anything..." })).toBeInTheDocument();
      expect(main().queryByText("The assistant is close to its usage limit")).not.toBeInTheDocument();
    });
  });
});
