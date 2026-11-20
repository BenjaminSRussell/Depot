import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { json, server, useFakeServer } from "../../shell/testing/fakeServer";
import { pageViolations } from "./a11yTest";
import { briefingItemRoute, briefingListSchema, type Briefing, type BriefingItem } from "./api/briefings";
import { BriefingCard } from "./BriefingCard";

useFakeServer();

const APPROVAL_ID = "6f1c1f0e-8f0a-4a52-9a43-0a8c5a1f2b11";
const BRIEFINGS_PATH = "/api/v1/agents/briefings";

const action: BriefingItem = {
  agentId: "cash_app",
  factId: "fact-1",
  text: "Apply the 4,200.00 USD payment from Acme",
  actionId: APPROVAL_ID,
  route: `/assistant/proposals/${APPROVAL_ID}`,
};

const briefing = (over: Partial<Briefing> = {}): Briefing => ({
  id: "0b9a6b5e-2f4c-4f6e-9c1d-3d2b1a0f9e88",
  status: "READY",
  narrative: true,
  createdAt: "2026-11-19T10:00:00.000Z",
  completedAt: "2026-11-19T10:00:05.000Z",
  readAt: null,
  changes: [{ agentId: "ap_coding", factId: "fact-0", text: "3 invoices were coded" }],
  risks: [{ agentId: "anomaly", factId: "fact-2", text: "One journal looks unusual" }],
  recommendedActions: [action],
  notes: [],
  ...over,
});

const list = (...items: Briefing[]) => json(200, { items, nextCursor: null, nextScheduledAt: null });

function renderCard() {
  return render(
    <MemoryRouter>
      <BriefingCard />
    </MemoryRouter>,
  );
}

describe("BriefingCard", () => {
  it("shows the generated time, the AI badge and three sections with source-agent labels", async () => {
    server.handler = () => list(briefing());
    renderCard();
    expect(await screen.findByRole("heading", { level: 2, name: "Your briefing" })).toBeInTheDocument();
    expect(screen.getByText("AI-generated: check before you use it")).toBeInTheDocument();
    expect(document.querySelector("time")).toHaveAttribute("datetime", "2026-11-19T10:00:05.000Z");
    for (const name of ["Changes (1)", "Risks (1)", "Recommended actions (1)"]) {
      expect(screen.getByRole("button", { name })).toBeInTheDocument();
    }
    expect(screen.getAllByRole("heading", { level: 3 })).toHaveLength(3);
    expect(screen.getByText("Cash application agent:")).toBeInTheDocument();
    expect(server.calls).toHaveLength(1);
    expect(server.calls[0]?.url.pathname + server.calls[0]?.url.search).toBe(`${BRIEFINGS_PATH}?limit=20`);
  });

  it("opens Recommended actions at once and toggles aria-expanded on the others", async () => {
    server.handler = () => list(briefing());
    renderCard();
    const actions = await screen.findByRole("button", { name: "Recommended actions (1)" });
    const risks = screen.getByRole("button", { name: "Risks (1)" });
    expect(actions).toHaveAttribute("aria-expanded", "true");
    expect(risks).toHaveAttribute("aria-expanded", "false");
    expect(screen.queryByText("One journal looks unusual")).not.toBeVisible();
    await userEvent.setup().click(risks);
    expect(risks).toHaveAttribute("aria-expanded", "true");
    expect(screen.getByText("One journal looks unusual")).toBeVisible();
  });

  it("links a recommended action to the proposal route and offers no control that decides or posts", async () => {
    server.handler = () => list(briefing());
    renderCard();
    const link = await screen.findByRole("link", { name: action.text });
    expect(link).toHaveAttribute("href", `/assistant/proposals/${APPROVAL_ID}`);
    expect(screen.queryByRole("button", { name: /approve|decline|post|pay/i })).toBeNull();
    expect(server.calls.every((call) => call.method === "GET")).toBe(true);
  });

  it("shows counts, links and a note without the AI badge when the briefing is deterministic", async () => {
    server.handler = () => list(briefing({ narrative: false }));
    renderCard();
    expect(await screen.findByText("Summaries are unavailable, so this briefing lists counts and links only.")).toBeInTheDocument();
    expect(screen.queryByText("AI-generated: check before you use it")).toBeNull();
    expect(screen.getByRole("button", { name: "Changes (1)" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: action.text })).toBeInTheDocument();
  });

  it("shows Nothing here for an empty section", async () => {
    server.handler = () => list(briefing({ recommendedActions: [] }));
    renderCard();
    expect(await screen.findByRole("button", { name: "Recommended actions (0)" })).toBeInTheDocument();
    expect(screen.getByText("Nothing here.")).toBeInTheDocument();
  });

  it("uses the newest READY briefing and skips RUNNING and FAILED ones", async () => {
    server.handler = () =>
      list(briefing({ status: "RUNNING", changes: [] }), briefing({ status: "FAILED", changes: [] }), briefing({ changes: [{ agentId: "ap_coding", factId: "f", text: "Ready one" }] }));
    renderCard();
    await screen.findByRole("button", { name: "Changes (1)" });
    await userEvent.setup().click(screen.getByRole("button", { name: "Changes (1)" }));
    expect(screen.getByText("Ready one")).toBeVisible();
  });

  it.each([
    ["an empty list", () => list()],
    ["only RUNNING and FAILED briefings", () => list(briefing({ status: "RUNNING" }), briefing({ status: "FAILED" }))],
  ])("renders nothing for %s", async (_label, reply) => {
    server.handler = reply;
    const { container } = renderCard();
    await vi.waitFor(() => expect(server.calls).toHaveLength(1));
    await vi.waitFor(() => expect(container).toBeEmptyDOMElement());
  });

  it("shows an inline error with Retry on a server failure and recovers on the second request", async () => {
    let attempts = 0;
    server.handler = () => (++attempts === 1 ? json(500, { code: "AGT-0500" }) : list(briefing()));
    renderCard();
    expect(await screen.findByRole("alert")).toHaveTextContent("Your briefing could not be loaded");
    expect(screen.queryByRole("dialog")).toBeNull();
    await userEvent.setup().click(screen.getByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("button", { name: "Recommended actions (1)" })).toBeInTheDocument();
    expect(server.calls).toHaveLength(2);
  });

  it("treats a body that fails the strict schema as an error and renders nothing from it", async () => {
    server.handler = () => json(200, { items: [{ ...briefing(), extra: true }], nextCursor: null, nextScheduledAt: null });
    renderCard();
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.queryByText("3 invoices were coded")).toBeNull();
  });

  it("has no axe violations in the narrative, deterministic and error states", async () => {
    server.handler = () => list(briefing());
    const narrative = renderCard();
    await screen.findByRole("button", { name: "Recommended actions (1)" });
    expect(pageViolations(narrative.container)).toEqual([]);
    narrative.unmount();

    server.handler = () => list(briefing({ narrative: false }));
    const deterministic = renderCard();
    await screen.findByText(/Summaries are unavailable/);
    expect(pageViolations(deterministic.container)).toEqual([]);
    deterministic.unmount();

    server.handler = () => json(500, { code: "AGT-0500" });
    const failed = renderCard();
    await screen.findByRole("alert");
    expect(pageViolations(failed.container)).toEqual([]);
    expect(within(failed.container).getByRole("button", { name: "Try again" })).toBeInTheDocument();
  });
});

describe("briefingItemRoute", () => {
  const item = (over: Partial<BriefingItem>): BriefingItem => ({ agentId: "ap_coding", factId: "f", text: "t", ...over });

  it("returns the proposal route for a valid route or a valid action id", () => {
    expect(briefingItemRoute(item({ route: `/assistant/proposals/${APPROVAL_ID}` }))).toBe(`/assistant/proposals/${APPROVAL_ID}`);
    expect(briefingItemRoute(item({ actionId: APPROVAL_ID }))).toBe(`/assistant/proposals/${APPROVAL_ID}`);
  });

  it.each([
    ["no route or action id", {}],
    ["an empty route", { route: "" }],
    ["a protocol-relative route", { route: "//evil.example/assistant/proposals/x" }],
    ["a script route", { route: "javascript:alert(1)" }],
    ["a route with a non-uuid id", { route: "/assistant/proposals/not-a-uuid" }],
    ["a route with a path-traversal id", { route: `/assistant/proposals/${APPROVAL_ID}/../../admin` }],
    ["a non-uuid action id", { actionId: "../admin" }],
  ])("returns no link for %s", (_label, over) => {
    expect(briefingItemRoute(item(over))).toBeUndefined();
  });

  it("falls back to a valid action id when the route is invalid", () => {
    expect(briefingItemRoute(item({ route: "//evil.example", actionId: APPROVAL_ID }))).toBe(`/assistant/proposals/${APPROVAL_ID}`);
  });
});

describe("briefingListSchema", () => {
  const body = { items: [briefing()], nextCursor: null, nextScheduledAt: null };

  it("accepts the documented shape", () => {
    expect(briefingListSchema.safeParse(body).success).toBe(true);
  });

  it.each([
    ["an unknown agent id", { ...body, items: [briefing({ changes: [{ ...action, agentId: "rogue" as BriefingItem["agentId"] }] })] }],
    ["a string narrative", { ...body, items: [{ ...briefing(), narrative: "true" }] }],
    ["a non-ISO createdAt", { ...body, items: [briefing({ createdAt: "yesterday" })] }],
    ["a missing items array", { nextCursor: null, nextScheduledAt: null }],
    ["a scheduled time", { ...body, nextScheduledAt: "2026-11-20T08:00:00.000Z" }],
  ])("rejects %s", (_label, bad) => {
    expect(briefingListSchema.safeParse(bad).success).toBe(false);
  });
});
