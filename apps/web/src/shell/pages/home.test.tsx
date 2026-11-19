import { cleanup, render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { describe, expect, it } from "vitest";
import { CopilotHome } from "../../modules/copilot/pages";
import { loadModules, type DepotModule } from "../../modules/registry";
import { Shell } from "../Shell";
import { json, pageViolations, server, useFakeServer } from "../testing/fakeServer";
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

function renderHome(duties: string[], shellModules: DepotModule[] = modules, entry: { pathname: string; state?: unknown } = { pathname: "/" }, features?: Record<string, boolean>) {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <Routes>
        <Route element={<Shell modules={shellModules} duties={duties} features={features} />}>
          <Route path="/" element={<HomePage />} />
          <Route path="/copilot" element={<CopilotHome />} />
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
          <Route element={<Shell modules={modules} duties={[]} identity="u1" />}>
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
    const askBox = () => main().getByRole("textbox", { name: "Ask or do anything..." });

    it("puts the prompt first in the main landmark and the workspace cards in a Browse workspaces region", () => {
      renderHome([COPILOT_DUTY]);
      expect(screen.getByRole("main").querySelector(PAGE_CONTROLS)).toBe(askBox());
      const region = within(main().getByRole("region", { name: "Browse workspaces" }));
      expect(region.getByText("Current workspace")).toBeInTheDocument();
      expect(region.getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
      expect(askBox().compareDocumentPosition(main().getByRole("region", { name: "Browse workspaces" })) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
    });

    it("shows an error and makes no request when the prompt is empty", async () => {
      const user = userEvent.setup();
      renderHome([COPILOT_DUTY]);
      await user.click(main().getByRole("button", { name: "Send" }));
      expect(main().getByRole("alert")).toHaveTextContent("Type a question or a request first.");
      expect(server.calls).toHaveLength(0);
    });

    it("opens the copilot in ask mode and sends the typed text once", async () => {
      const user = userEvent.setup();
      server.handler = (url) =>
        url.pathname === "/api/v1/copilot/chat"
          ? json(200, { answer: "Cash is steady.", citations: [], toolCalls: [], refusal: null, generated: true })
          : undefined;
      renderHome([COPILOT_DUTY]);
      await user.type(askBox(), "how is cash{Enter}");
      expect(await main().findByRole("article", { name: "Copilot answer" })).toHaveTextContent("Cash is steady.");
      const chats = server.calls.filter((c) => c.url.pathname === "/api/v1/copilot/chat");
      expect(chats).toHaveLength(1);
      expect(chats[0].body).toEqual({ mode: "ask", message: "how is cash" });
    });

    it("announces the sending text in the shell status region so it survives the route change", async () => {
      const user = userEvent.setup();
      server.handler = (url) =>
        url.pathname === "/api/v1/copilot/chat"
          ? json(200, { answer: "Cash is steady.", citations: [], toolCalls: [], refusal: null, generated: true })
          : undefined;
      renderHome([COPILOT_DUTY]);
      await user.type(askBox(), "how is cash{Enter}");
      await main().findByRole("article", { name: "Copilot answer" });
      expect(screen.queryByRole("textbox", { name: "Ask or do anything..." })).not.toBeInTheDocument();
      expect(screen.getAllByRole("status").some((r) => r.textContent === "Sending your question to the assistant...")).toBe(true);
    });

    it.each([
      ["a number", { prompt: 5 }],
      ["an object", { prompt: {} }],
      ["an empty string", { prompt: "" }],
      ["no prompt", {}],
      ["no state", null],
    ])("sends nothing when the copilot opens with %s as its prompt", async (_name, state) => {
      renderHome([COPILOT_DUTY], modules, { pathname: "/copilot", state });
      await screen.findByRole("heading", { name: "Copilot" });
      expect(server.calls.filter((c) => c.url.pathname === "/api/v1/copilot/chat")).toHaveLength(0);
    });

    it("passes an oversize prompt through so the copilot shows the refusal instead of dropping it", async () => {
      const prompt = "x".repeat(4500);
      server.handler = (url) =>
        url.pathname === "/api/v1/copilot/chat"
          ? json(200, { answer: "", citations: [], toolCalls: [], refusal: { code: "AGT-0001", message: "That question is too long." }, generated: true })
          : undefined;
      renderHome([COPILOT_DUTY], modules, { pathname: "/copilot", state: { prompt } });
      expect(await main().findByText("That question is too long.")).toBeInTheDocument();
      expect(server.calls.filter((c) => c.url.pathname === "/api/v1/copilot/chat")[0].body).toEqual({ mode: "ask", message: prompt });
    });

    it("shows the classic cards as the primary content, with no prompt and no copilot request, without the duty", () => {
      renderHome([]);
      expect(screen.queryByRole("textbox", { name: "Ask or do anything..." })).not.toBeInTheDocument();
      expect(main().queryByRole("button", { name: "Send" })).not.toBeInTheDocument();
      expect(main().queryByRole("region", { name: "Browse workspaces" })).not.toBeInTheDocument();
      expect(main().getByText("Current workspace")).toBeInTheDocument();
      expect(main().getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
      expect(server.calls.filter((c) => c.url.pathname.startsWith("/api/v1/copilot"))).toHaveLength(0);
    });

    it("shows the classic home, with no prompt to drop, when the copilot is switched off for the installation", () => {
      renderHome([COPILOT_DUTY], modules, { pathname: "/" }, { "install:node": false });
      expect(screen.queryByRole("textbox", { name: "Ask or do anything..." })).not.toBeInTheDocument();
      expect(main().queryByRole("button", { name: "Send" })).not.toBeInTheDocument();
      expect(main().getByRole("link", { name: "See all features" })).toHaveAttribute("href", "/features");
    });

    it("offers a workspace page as a link, one click away, inside the secondary region", () => {
      renderHome([COPILOT_DUTY]);
      const region = within(main().getByRole("region", { name: "Browse workspaces" }));
      const tasks = region.getAllByRole("list", { name: /Top tasks/ })[0];
      expect(within(tasks).getAllByRole("link")[0]).toHaveAttribute("href", expect.stringMatching(/^\//));
    });
  });
});
