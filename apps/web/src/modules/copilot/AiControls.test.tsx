import { render, screen, waitFor, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MemoryRouter } from "react-router-dom";
import { describe, expect, it, vi } from "vitest";
import { pageViolations } from "./a11yTest";
import { AgentsPage } from "./AgentsPage";
import {
  BUDGETS_PATH,
  DEFINITIONS_PATH,
  getAgentDefinitions,
  getBudgets,
  putBudgetCap,
  setAgentEnabled,
  type AgentDefinition,
  type AiDataPolicy,
  type BudgetEntry,
  type BudgetList,
} from "./api/agents";
import { BudgetsPage, TENANT_ROW_HINT } from "./BudgetsPage";

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "content-type": status === 200 ? "application/json" : "application/problem+json" } });

const entry = (agentId: string, status: BudgetEntry["status"] = "OK", usedIn = 100): BudgetEntry => ({
  scope: "AGENT",
  agentId,
  cap: { tokensIn: 1000, tokensOut: 500 },
  tokensUsed: { tokensIn: usedIn, tokensOut: 50 },
  costUsed: "0.001200",
  percentConsumed: 10,
  status,
});

const budgets: BudgetList = {
  month: "2026-11",
  tenant: { ...entry("x", "SOFT", 850), scope: "TENANT", agentId: null },
  agents: [entry("briefing"), entry("ap_match", "HARD", 1200)],
};

const policy: AiDataPolicy = {
  version: 1,
  modules: {
    ap: { P0: true, P1: true, P2: false, P3: false, CUI: false },
    ar: { P0: true, P1: true, P2: false, P3: false, CUI: false },
    platform: { P0: true, P1: true, P2: false, P3: false, CUI: false },
  },
};

const definitions: AgentDefinition[] = [
  { agentId: "briefing", version: 1, enabled: true, disabledBy: null, disabledAt: null, reason: null },
  { agentId: "ap_match", version: 2, enabled: false, disabledBy: "u1", disabledAt: "2026-11-19T10:00:00Z", reason: null },
];

function mount(ui: React.ReactElement) {
  return render(<MemoryRouter>{ui}</MemoryRouter>);
}

describe("agent budget and definition clients", () => {
  it("reads budgets, puts a cap and validates the response", async () => {
    const fetchFn = vi.fn(async (_url: string, _init?: RequestInit) => json(budgets));
    expect(await getBudgets(undefined, fetchFn)).toEqual(budgets);
    expect(fetchFn.mock.calls[0]?.[0]).toBe(BUDGETS_PATH);
    await expect(getBudgets(undefined, async () => json({ month: 1 }))).rejects.toThrow("couldn't read");

    const put = vi.fn(async (_url: string, _init?: RequestInit) => json(entry("briefing")));
    await putBudgetCap("briefing", { limitIn: 5, limitOut: 6 }, put);
    expect(put.mock.calls[0]?.[0]).toBe(`${BUDGETS_PATH}/briefing`);
    expect(put.mock.calls[0]?.[1]?.method).toBe("PUT");
    expect(JSON.parse(String(put.mock.calls[0]?.[1]?.body))).toEqual({ limitIn: 5, limitOut: 6 });
  });

  it("lists definitions and posts disable with a body and enable without one", async () => {
    expect(await getAgentDefinitions(undefined, async () => json({ items: definitions }))).toEqual(definitions);
    const fetchFn = vi.fn(async (_url: string, _init?: RequestInit) => json(definitions[0]));
    await setAgentEnabled("briefing", false, fetchFn);
    await setAgentEnabled("briefing", true, fetchFn);
    expect(fetchFn.mock.calls[0]?.[0]).toBe(`${DEFINITIONS_PATH}/briefing:disable`);
    expect(fetchFn.mock.calls[0]?.[1]?.body).toBe("{}");
    expect(fetchFn.mock.calls[1]?.[0]).toBe(`${DEFINITIONS_PATH}/briefing:enable`);
    expect(fetchFn.mock.calls[1]?.[1]?.body).toBeUndefined();
  });
});

describe("BudgetsPage", () => {
  const writer = ["agentbudget.read", "agentbudget.write"];

  it("shows an error state with Try again when the endpoint fails", async () => {
    const load = vi.fn().mockRejectedValueOnce(new Error("boom")).mockResolvedValue(budgets);
    mount(<BudgetsPage load={load} duties={writer} />);
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
    expect(await screen.findByRole("table", { name: "Token budgets for 2026-11" })).toBeInTheDocument();
  });

  it("shows caps, use, percent, status badges and the read-only tenant row", async () => {
    mount(<BudgetsPage load={async () => budgets} duties={writer} />);
    const table = await screen.findByRole("table", { name: "Token budgets for 2026-11" });
    expect(within(table).getByText("HARD")).toBeInTheDocument();
    expect(within(table).getByText("SOFT")).toBeInTheDocument();
    expect(within(table).getAllByText("10%").length).toBeGreaterThan(0);
    expect(screen.getByText(TENANT_ROW_HINT)).toBeInTheDocument();
    expect(screen.queryByLabelText(/Input token cap for x/)).toBeNull();
    expect(pageViolations()).toEqual([]);
  });

  it("makes cap fields read-only without agentbudget.write and offers no save", async () => {
    mount(<BudgetsPage load={async () => budgets} duties={["agentbudget.read"]} />);
    expect(await screen.findByLabelText("Input token cap for briefing")).toHaveAttribute("readonly");
    expect(screen.queryByRole("button", { name: /Save caps/ })).toBeNull();
    expect(screen.queryByText("Data the assistant may use")).toBeNull();
  });

  it("rejects a non-numeric or negative cap inline and does not call the endpoint", async () => {
    const save = vi.fn();
    mount(<BudgetsPage load={async () => budgets} save={save} duties={writer} />);
    const input = await screen.findByLabelText("Input token cap for briefing");
    await userEvent.clear(input);
    await userEvent.type(input, "-5");
    await userEvent.click(screen.getByRole("button", { name: "Save caps for briefing" }));
    expect(await screen.findByText("Enter a whole number of tokens, zero or more.")).toBeInTheDocument();
    expect(input).toHaveAttribute("aria-invalid", "true");
    expect(save).not.toHaveBeenCalled();
  });

  it("saves a valid cap and reloads", async () => {
    const save = vi.fn(async () => entry("briefing"));
    const load = vi.fn(async () => budgets);
    mount(<BudgetsPage load={load} save={save} duties={writer} />);
    const input = await screen.findByLabelText("Input token cap for briefing");
    await userEvent.clear(input);
    await userEvent.type(input, "2000");
    await userEvent.click(screen.getByRole("button", { name: "Save caps for briefing" }));
    await waitFor(() => expect(save).toHaveBeenCalledWith("briefing", { limitIn: 2000, limitOut: 500 }));
    await waitFor(() => expect(load).toHaveBeenCalledTimes(2));
  });

  it("warns in a dialog before saving a cap below current burn and saves only on confirm", async () => {
    const save = vi.fn(async () => entry("ap_match"));
    mount(<BudgetsPage load={async () => budgets} save={save} duties={writer} />);
    const input = await screen.findByLabelText("Input token cap for ap_match");
    await userEvent.clear(input);
    await userEvent.type(input, "10");
    await userEvent.click(screen.getByRole("button", { name: "Save caps for ap_match" }));
    expect(save).not.toHaveBeenCalled();
    const dialog = await screen.findByRole("alertdialog");
    expect(pageViolations()).toEqual([]);
    await userEvent.click(within(dialog).getByRole("button", { name: "Save cap" }));
    await waitFor(() => expect(save).toHaveBeenCalledWith("ap_match", { limitIn: 10, limitOut: 500 }));
  });

  it("shows AGT-0422 inline on the field", async () => {
    const refused = Object.assign(new Error("limitOut must be a non-negative integer"), { code: "AGT-0422" });
    mount(<BudgetsPage load={async () => budgets} save={async () => Promise.reject(refused)} duties={writer} />);
    await screen.findByLabelText("Output token cap for briefing");
    await userEvent.click(screen.getByRole("button", { name: "Save caps for briefing" }));
    expect(await screen.findByText("limitOut must be a non-negative integer")).toBeInTheDocument();
  });

  it("shows the data policy summary with a planned-route link only with aipolicy.read", async () => {
    mount(<BudgetsPage load={async () => budgets} loadPolicy={async () => policy} duties={[...writer, "aipolicy.read"]} />);
    expect(await screen.findByRole("heading", { name: "Data the assistant may use" })).toBeInTheDocument();
    expect(await screen.findAllByText(/May reach AI: P0, P1\. Denied: P2, P3, CUI\./)).toHaveLength(3);
    expect(screen.getByRole("link", { name: "Open the data policy page" })).toHaveAttribute("href", "/copilot/ai-policy");
  });
});

describe("AgentsPage", () => {
  it("lists agents, confirms before disabling and enables without a dialog", async () => {
    const toggle = vi.fn(async (agentId: string) => ({ ...definitions[0]!, agentId }));
    mount(<AgentsPage load={async () => definitions} toggle={toggle} />);
    const table = await screen.findByRole("table", { name: "Agents" });
    expect(within(table).getByText("Disabled")).toBeInTheDocument();
    await userEvent.click(screen.getByRole("button", { name: "Disable briefing" }));
    const dialog = await screen.findByRole("alertdialog");
    expect(toggle).not.toHaveBeenCalled();
    expect(pageViolations()).toEqual([]);
    await userEvent.click(within(dialog).getByRole("button", { name: "Disable agent" }));
    await waitFor(() => expect(toggle).toHaveBeenCalledWith("briefing", false));
    await userEvent.click(screen.getByRole("button", { name: "Enable ap_match" }));
    await waitFor(() => expect(toggle).toHaveBeenCalledWith("ap_match", true));
  });

  it("cancelling the dialog calls nothing, and a failed load offers Try again", async () => {
    const toggle = vi.fn();
    const load = vi.fn().mockRejectedValueOnce(new Error("down")).mockResolvedValue(definitions);
    mount(<AgentsPage load={load} toggle={toggle} />);
    await userEvent.click(await screen.findByRole("button", { name: "Try again" }));
    await userEvent.click(await screen.findByRole("button", { name: "Disable briefing" }));
    await userEvent.click(within(await screen.findByRole("alertdialog")).getByRole("button", { name: "Cancel" }));
    expect(toggle).not.toHaveBeenCalled();
  });

  it("shows a failure when the change is refused", async () => {
    mount(<AgentsPage load={async () => definitions} toggle={async () => Promise.reject(new Error("you do not hold duty agents.admin"))} />);
    await userEvent.click(await screen.findByRole("button", { name: "Enable ap_match" }));
    expect(await screen.findByText("you do not hold duty agents.admin")).toBeInTheDocument();
  });
});
