import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Instruction, RecipeDocument } from "./api/client";
import { nextIndex, flattenSteps, runBranch, runCalculation, valuesAfter, checkEntry, messageFor } from "./piSheet";
import { clockText, PiSheetRunner } from "./PiSheetRunner";

const doc = (instructions: Instruction[]): RecipeDocument => ({ procedure: { name: "B", unitProcedures: [{ name: "U", operations: [{ name: "O", phases: [{ name: "P", instructions: instructions.map((x, i) => ({ ...x, seq: i + 1 })) }] }] }] } });
const num = (name: string): Instruction["characteristic"] => ({ name, type: "numeric", unit: "kg", min: 0, max: 100 });
const entryStep = (v: string): Instruction => ({ seq: 0, category: "process-data-request", text: `Enter ${v}.`, var: v, characteristic: num(v) });

let reading: { status: number; body: unknown };
beforeEach(() => {
  reading = { status: 404, body: { code: "SF-0196", detail: "no recent reading" } };
  vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify(reading.body), { status: reading.status, headers: { "Content-Type": "application/json" } })));
});
afterEach(() => {
  vi.unstubAllGlobals();
  vi.useRealTimers();
});

const mount = (d: RecipeDocument, extra: Partial<Parameters<typeof PiSheetRunner>[0]> = {}) => {
  const recorded: { sourceMessageId: string; values: { name: string; value: string }[] }[] = [];
  const onRecord = vi.fn((m: (typeof recorded)[number]) => void recorded.push(m));
  render(
    <MemoryRouter>
      <PiSheetRunner doc={d} recipeId="rec-1" onRecord={onRecord} onComplete={vi.fn()} {...extra} />
    </MemoryRouter>,
  );
  return recorded;
};
const next = () => fireEvent.click(screen.getByRole("button", { name: /Next step|Finish|Done, next step/ }));
const type = (label: string, value: string) => fireEvent.change(screen.getByLabelText(label), { target: { value } });

describe("running the step library's kinds: rules", () => {
  it("works out a calculation from earlier values and explains one it cannot", () => {
    const calc: Instruction = { seq: 3, category: "calculation", text: "Net", expression: "gross - tare", var: "net" };
    expect(runCalculation(calc, { gross: 12.5, tare: 0.3 })).toEqual({ ok: true, value: "12.2" });
    const missing = runCalculation(calc, { gross: 1 });
    expect(missing.ok).toBe(false);
    expect(!missing.ok && missing.message).toMatch(/tare is not known.*skipped/);
    expect(runCalculation({ ...calc, expression: "1 / zero" }, { zero: 0 })).toEqual({ ok: false, message: "Division by zero." });
    expect(valuesAfter(calc, { value: "12.2", comment: "" }, { gross: 1 })).toEqual({ gross: 1, net: 12.2 });
    expect(valuesAfter({ ...entryStep("ok"), characteristic: { name: "ok", type: "boolean" } }, { value: "true", comment: "" }, {})).toEqual({ ok: 1 });
    expect(valuesAfter({ seq: 1, category: "process-parameter-setpoint", value: 72, var: "sp", characteristic: num("Jacket") }, undefined, {})).toEqual({ sp: 72 });
  });

  it("jumps a branch to its target step and skips the steps between", () => {
    const d = doc([entryStep("t"), { seq: 0, category: "branch", text: "Hot?", condition: "t > 80", target: 4 }, { seq: 0, category: "instruction-text", text: "Cool." }, { seq: 0, category: "instruction-text", text: "Go." }]);
    const steps = flattenSteps(d);
    expect(runBranch(steps[1]!.instruction, { t: 90 })).toEqual({ ok: true, jump: true });
    expect(nextIndex(steps, 1, true)).toBe(3);
    expect(nextIndex(steps, 1, false)).toBe(2);
    expect(nextIndex(steps, 0, true)).toBe(1);
    expect(runBranch(steps[1]!.instruction, {}).ok).toBe(false);
  });

  it("checks a machine readout against its limits, its tolerance and the rule for a hand entry", () => {
    const ro: Instruction = { seq: 1, category: "machine-readout", node: "n", value: 180, tolerance: 5, characteristic: { name: "Temp", type: "numeric", unit: "degC", min: 150, max: 210 } };
    expect(checkEntry(ro, { value: "182", comment: "" })).toEqual({ ok: true, outOfLimit: false });
    expect(checkEntry(ro, { value: "190", comment: "" }).ok).toBe(false);
    expect(checkEntry(ro, { value: "190", comment: "drift" })).toEqual({ ok: true, outOfLimit: true });
    expect(checkEntry(ro, { value: "180", comment: "", manual: true }).ok).toBe(false);
    expect(checkEntry(ro, { value: "180", comment: "probe offline", manual: true })).toEqual({ ok: true, outOfLimit: false });
    const msg = messageFor("r", flattenSteps(doc([ro]))[0]!, { value: "180", comment: "probe offline", manual: true });
    expect(msg?.values.map((v) => v.name)).toEqual(["Temp", "readout-source", "node", "expected", "tolerance", "comment"]);
    expect(msg?.values[1]?.value).toBe("entered-by-hand");
    expect(clockText(61.2)).toBe("1:02");
  });
});

describe("running the step library's kinds: the kiosk", () => {
  it("shows a calculation's result and records it", async () => {
    const recorded = mount(doc([entryStep("gross"), entryStep("tare"), { seq: 0, category: "calculation", text: "Net weight.", var: "net", expression: "gross - tare" }]));
    type("gross (kg)", "12.5");
    next();
    await screen.findByLabelText("tare (kg)");
    type("tare (kg)", "0.3");
    next();
    expect(await screen.findByText("Result (net): 12.2")).toBeInTheDocument();
    next();
    await waitFor(() => expect(recorded).toHaveLength(3));
    expect(recorded[2]!.values).toEqual([{ name: "net", type: "numeric", value: "12.2" }, { name: "expression", type: "text", value: "gross - tare" }]);
  });

  it("holds the next step while a blocking timer runs and lets it go when it has run out", async () => {
    let t = 1_000_000;
    vi.useFakeTimers({ shouldAdvanceTime: false });
    const recorded = mount(doc([{ seq: 0, category: "timer", text: "Let it cure.", durationSeconds: 90, blocking: true }, { seq: 0, category: "instruction-text", text: "Done." }]), { clock: () => t });
    expect(screen.getByRole("timer")).toHaveTextContent("1:30 left");
    expect(screen.getByRole("button", { name: "Next step" })).toBeDisabled();
    await act(async () => {
      t += 91_000;
      vi.advanceTimersByTime(1000);
    });
    expect(screen.getByRole("timer")).toHaveTextContent("The timer has run out.");
    expect(screen.getByRole("button", { name: "Next step" })).toBeEnabled();
    vi.useRealTimers();
    next();
    await waitFor(() => expect(recorded).toHaveLength(1));
    expect(recorded[0]!.values.map((v) => v.name)).toEqual(["timer-seconds", "timer-blocking"]);
  });

  it("does not hold the next step for a timer that is not blocking, and shows it running", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: false });
    mount(doc([{ seq: 0, category: "timer", text: "Soak.", durationSeconds: 600, blocking: false }, { seq: 0, category: "instruction-text", text: "Meanwhile." }]), { clock: () => 5_000_000 });
    expect(screen.getByRole("button", { name: "Next step" })).toBeEnabled();
    vi.useRealTimers();
    next();
    expect(await screen.findByText("Meanwhile.")).toBeInTheDocument();
    expect(screen.getByRole("list", { name: "Running timers" })).toHaveTextContent("Soak.: 10:00 left");
  });

  it("fetches a machine readout from the connector's reading", async () => {
    reading = { status: 200, body: { node: "n", value: "181.5", nodeType: "Double", readAt: "2026-11-15T10:00:00Z", ageSeconds: 4 } };
    const recorded = mount(doc([{ seq: 0, category: "machine-readout", text: "Oven.", node: "ns=2;s=Oven.Temp", characteristic: { name: "Temp", type: "numeric", unit: "degC", min: 150, max: 210 } }]));
    await waitFor(() => expect(screen.getByLabelText("Temp (degC)")).toHaveValue("181.5"));
    expect(screen.getByLabelText("Temp (degC)")).toHaveAttribute("readonly");
    next();
    await waitFor(() => expect(recorded).toHaveLength(1));
    expect(recorded[0]!.values.find((v) => v.name === "readout-source")?.value).toBe("machine");
  });

  it("takes a hand entry with a reason when the connector has no reading", async () => {
    const recorded = mount(doc([{ seq: 0, category: "machine-readout", text: "Oven.", node: "ns=2;s=Oven.Temp", characteristic: { name: "Temp", type: "numeric", unit: "degC" } }]));
    expect(await screen.findByText("No recent reading from the machine")).toBeInTheDocument();
    type("Temp (degC)", "180");
    next();
    expect((await screen.findAllByText("Say why the reading was entered by hand.")).length).toBeGreaterThan(0);
    type("Comment", "probe offline");
    next();
    await waitFor(() => expect(recorded).toHaveLength(1));
    expect(recorded[0]!.values.find((v) => v.name === "readout-source")?.value).toBe("entered-by-hand");
  });

  it("jumps a branch and skips the steps between", async () => {
    const recorded = mount(doc([entryStep("temp"), { seq: 0, category: "branch", text: "Too hot?", condition: "temp > 80", target: 4 }, { seq: 0, category: "instruction-text", text: "Cool the batch." }, { seq: 0, category: "instruction-text", text: "Carry on." }]));
    type("temp (kg)", "90");
    next();
    expect(await screen.findByText("The condition is met: the sheet goes on at step 4.")).toBeInTheDocument();
    next();
    expect(await screen.findByText("Carry on.")).toBeInTheDocument();
    expect(screen.queryByText("Cool the batch.")).toBeNull();
    expect(recorded[1]!.values).toEqual([{ name: "branch-taken", type: "boolean", value: "true" }, { name: "branch-target", type: "numeric", value: "4" }]);
  });

  it("goes on with the next step when the condition is not met, and refuses a calculation that has no value", async () => {
    mount(doc([entryStep("temp"), { seq: 0, category: "branch", text: "Too hot?", condition: "temp > 80", target: 4 }, { seq: 0, category: "instruction-text", text: "Cool the batch." }, { seq: 0, category: "instruction-text", text: "Carry on." }]));
    type("temp (kg)", "20");
    next();
    expect(await screen.findByText("The condition is not met: the sheet goes on with the next step.")).toBeInTheDocument();
    next();
    expect(await screen.findByText("Cool the batch.")).toBeInTheDocument();
  });

  it("shows why a calculation cannot be worked out and holds the step", async () => {
    mount(doc([{ seq: 0, category: "calculation", text: "Net.", expression: "gross - tare" }]));
    expect(await screen.findByText("This step cannot be calculated")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Finish" })).toBeDisabled();
  });
});
