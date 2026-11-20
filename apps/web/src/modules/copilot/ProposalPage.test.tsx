import { render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, describe, expect, it, vi } from "vitest";
import { PrincipalProvider } from "../../shell/principal";
import { pageViolations } from "./a11yTest";
import type { Approval } from "./api/proposals";
import { ProposalPage, type ProposalLoad } from "./ProposalPage";

const base: Approval = {
  runId: "00000000-0000-7000-8000-0000000000d1",
  actionId: "00000000-0000-7000-8000-00000000a001",
  tool: "depot.ar.settlement.propose",
  state: "PENDING",
  amount: { amount: "1200.50", currency: "USD" },
  requestedAt: "2026-11-19T10:00:00.000Z",
  expiresAt: "2026-11-22T10:00:00.000Z",
};

const decidedAt = "2026-11-19T11:00:00.000Z";
const stateCases: Array<[string, Approval, string]> = [
  ["PENDING", base, "Awaiting your approval"],
  ["APPROVED", { ...base, state: "APPROVED", decided: { decision: "APPROVED", decidedAt } }, "Approved"],
  ["REJECTED", { ...base, state: "REJECTED", decided: { decision: "REJECTED", decidedAt } }, "Declined"],
  ["EXPIRED", { ...base, state: "EXPIRED" }, "Expired"],
];

const show = (load: (signal: AbortSignal) => Promise<ProposalLoad>) =>
  render(
    <MemoryRouter>
      <PrincipalProvider duties={["agents.run.read"]}>
        <ProposalPage approvalId={base.actionId} load={load} />
      </PrincipalProvider>
    </MemoryRouter>,
  );

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("ProposalPage", () => {
  it.each(stateCases)("%s: shows the card with the action, amount and state and announces the state", async (_state, approval, label) => {
    const { container } = show(async () => approval);
    expect(await screen.findByRole("group", { name: "AI suggestion" })).toHaveAttribute("data-host", "proposal");
    expect(screen.getAllByText("Propose on ar settlement").length).toBeGreaterThan(0);
    expect(screen.getByText("$1,200.50")).toBeInTheDocument();
    expect(screen.getAllByText(label).length).toBeGreaterThan(0);
    expect(screen.getByRole("status")).toHaveTextContent(`Proposal status: ${label}.`);
    expect(screen.getByText("Details are not available after you leave this page.")).toBeInTheDocument();
    expect(screen.queryByText(base.runId)).toBeNull();
    expect(screen.queryByText(base.actionId)).toBeNull();
    expect(screen.queryByRole("button", { name: /approve|decline/i })).toBeNull();
    expect(pageViolations(container)).toEqual([]);
  });

  it("offers Ask again and no approve control for an expired proposal, and not for a pending one", async () => {
    const expired = show(async () => ({ ...base, state: "EXPIRED" }));
    expect(await screen.findByRole("link", { name: "Ask again" })).toHaveAttribute("href", "/");
    expect(screen.queryByRole("button", { name: /approve/i })).toBeNull();
    expired.unmount();
    show(async () => base);
    await screen.findByRole("group", { name: "AI suggestion" });
    expect(screen.queryByRole("link", { name: "Ask again" })).toBeNull();
  });

  it("shows the same not-found text for an unknown id and for one the server hides, with no sign it exists", async () => {
    const problem = (detail: string) => new Response(JSON.stringify({ code: "AGT-0404", detail }), { status: 404 });
    const texts: string[] = [];
    for (const detail of ["no approval request for this action", "you may not see this"]) {
      vi.stubGlobal("fetch", vi.fn(async () => problem(detail)));
      const view = render(
        <MemoryRouter>
          <ProposalPage approvalId={base.actionId} />
        </MemoryRouter>,
      );
      await screen.findByRole("heading", { name: "We couldn't find that proposal" });
      texts.push(view.container.textContent ?? "");
      expect(pageViolations(view.container)).toEqual([]);
      view.unmount();
    }
    expect(texts[0]).toBe(texts[1]);
    expect(texts[0]).not.toMatch(/AGT-|forbidden|permission/i);
  });

  it("shows a load error, not not-found, when the load fails, and retries", async () => {
    const load = vi.fn<(signal: AbortSignal) => Promise<ProposalLoad>>().mockRejectedValueOnce(new Error("HTTP 500: internal detail")).mockResolvedValue(base);
    show(load);
    expect(await screen.findByText("We couldn't load this proposal")).toBeInTheDocument();
    expect(screen.queryByText(/internal detail/)).toBeNull();
    expect(screen.queryByText("We couldn't find that proposal")).toBeNull();
    screen.getByRole("button", { name: "Try again" }).click();
    await waitFor(() => expect(screen.getByRole("group", { name: "AI suggestion" })).toBeInTheDocument());
  });

  it("moves focus to the result once it has loaded", async () => {
    const { container } = show(async () => base);
    await screen.findByRole("group", { name: "AI suggestion" });
    await waitFor(() => expect(container.contains(document.activeElement) && document.activeElement !== document.body).toBe(true));
  });
});
