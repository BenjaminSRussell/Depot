import { matchRoutes, type RouteObject } from "react-router-dom";
import budgets from "../../e2e/taskBudgets.json";
import { loadModules } from "../modules/registry";
import plannedRoutes from "./plannedRoutes.json";
import tasksJson from "./usabilityTasks.json";
import workspaces from "./workspaces/workspaces.json";

interface Task {
  id: string;
  persona: string;
  route: string;
  module: string;
}

interface Planned {
  route: string;
  reason: string;
  issue: unknown;
  slice: unknown;
}

const PROGRAMME_PERSONAS = ["operator", "master-data", "approver", "maintenance", "auditor", "technician", "customer-portal", "supplier-portal", "new-user"];

const sources = import.meta.glob<string>(
  ["../../../customer-portal/src/App.tsx", "../../../supplier-portal/src/App.tsx", "../../../supplier-portal/src/main.tsx"],
  { query: "?raw", import: "default", eager: true },
);

const taskDocs = import.meta.glob<string>("../../docs/usability-tasks.md", { query: "?raw", import: "default", eager: true });
const taskDoc = Object.values(taskDocs).join("\n");
const alignedDocs = import.meta.glob<string>("../../docs/*.md", { query: "?raw", import: "default", eager: true });
const whatsNew = Object.values(import.meta.glob<string>("../../../../docs/user/whats-new.md", { query: "?raw", import: "default", eager: true })).join("\n");

function appRoutes(app: string): RouteObject[] {
  return Object.entries(sources)
    .filter(([file]) => file.includes(`/${app}/`))
    .flatMap(([, source]) => [...source.matchAll(/path="([^"*]+)"/g)].map((m) => ({ path: m[1] })));
}

const serving: Record<string, RouteObject[]> = {
  ...Object.fromEntries(loadModules().map((m) => [m.id, m.routes])),
  "customer-portal": appRoutes("customer-portal"),
  "supplier-portal": appRoutes("supplier-portal"),
  technician: [{ path: "/technician" }],
  device: [{ path: "/device" }],
  shell: [{ path: "/" }],
};

const tasks: Task[] = Object.entries(tasksJson.personas).flatMap(([persona, { tasks: byId }]) =>
  Object.entries(byId).map(([id, [route, module]]) => ({ id, persona, route, module })),
);

const positive = (value: unknown) => Number.isInteger(value) && (value as number) > 0;

function problems(taskList: Task[], plannedList: Planned[], routesByModule: Record<string, RouteObject[]>): string[] {
  const serves = (module: string, route: string) => matchRoutes(routesByModule[module] ?? [], route) !== null;
  const found: string[] = [];
  const seen = new Set<string>();
  for (const task of taskList) {
    if (seen.has(task.id)) found.push(`${task.id} is listed twice`);
    seen.add(task.id);
    const planned = plannedList.some((p) => p.route === task.route);
    if (!planned && !serves(task.module, task.route)) {
      found.push(`${task.id} starts at ${task.route}, which module ${task.module} does not serve and plannedRoutes.json does not list`);
    }
  }
  for (const planned of plannedList) {
    if (!positive(planned.issue)) found.push(`planned route ${planned.route} has no issue number`);
    if (!positive(planned.slice)) found.push(`planned route ${planned.route} has no slice`);
    if (Object.keys(routesByModule).some((module) => serves(module, planned.route))) {
      found.push(`planned route ${planned.route} exists now, remove it from plannedRoutes.json`);
    }
  }
  return found;
}

describe("usability task start routes (G6)", () => {
  it("covers all 108 task ids", () => {
    expect(tasks).toHaveLength(108);
  });

  it("starts every task on a route the table serves or a planned route with an issue", () => {
    expect(problems(tasks, plannedRoutes, serving)).toEqual([]);
  });

  it("belongs every persona to a workspace or a programme persona", () => {
    const known = new Set([...workspaces.workspaces.map((w) => w.id), ...PROGRAMME_PERSONAS]);
    expect(Object.keys(tasksJson.personas).filter((p) => !known.has(p))).toEqual([]);
  });

  it("flags a route that is neither served nor planned", () => {
    const broken = [...tasks, { id: "ZZ1", persona: "x", route: "/no/such/page", module: "ledger" }];
    expect(problems(broken, plannedRoutes, serving)).toEqual([expect.stringContaining("ZZ1 starts at /no/such/page")]);
  });

  it("flags a task whose module does not serve its route", () => {
    expect(problems([{ id: "ZZ1", persona: "x", route: "/ledger/open-items", module: "payables" }], [], serving)).toHaveLength(1);
  });

  it("flags a duplicate task id", () => {
    expect(problems([tasks[0], tasks[0]], plannedRoutes, serving)).toEqual([`${tasks[0].id} is listed twice`]);
  });

  it("flags a planned route without an issue or slice", () => {
    const planned = [{ route: "/later", reason: "r", issue: "691", slice: undefined }];
    expect(problems([], planned, serving)).toEqual(["planned route /later has no issue number", "planned route /later has no slice"]);
  });

  it("flags a planned route that now exists", () => {
    const planned = [{ route: "/ledger/open-items", reason: "r", issue: 1, slice: 1 }];
    expect(problems([], planned, serving)).toEqual([expect.stringContaining("exists now")]);
  });

  it("has a row in usability-tasks.md for every task id", () => {
    const missing = tasks.filter((t) => !new RegExp(`^\\| ${t.id} \\|`, "m").test(taskDoc)).map((t) => t.id);
    expect(missing).toEqual([]);
  });

  it("lists CT4 and WS1 as planned routes", () => {
    const planned = tasks.filter((t) => ["CT4", "WS1"].includes(t.id)).map((t) => plannedRoutes.some((p) => p.route === t.route));
    expect(planned).toEqual([true, true]);
  });
});

describe("task budgets (G5 data)", () => {
  const known = new Set(tasks.map((t) => t.id));
  const entries = Object.entries(budgets as Record<string, { budget: unknown; round0: unknown; script: unknown; reason?: unknown }>);

  it("names only known tasks", () => {
    expect(entries.map(([id]) => id).filter((id) => !known.has(id))).toEqual([]);
  });

  it("gives each entry a numeric budget or none, and either a script with a Round 0 count or a reason", () => {
    const bad = entries
      .filter(([, e]) => {
        const budgetOk = e.budget === null || (Number.isInteger(e.budget) && (e.budget as number) >= 0);
        const scripted = typeof e.script === "string" && e.script !== "" && positive(e.round0);
        const unscripted = e.script === null && e.round0 === null && typeof e.reason === "string" && e.reason !== "";
        return !(budgetOk && (scripted || unscripted));
      })
      .map(([id]) => id);
    expect(bad).toEqual([]);
  });
});

describe("documentation alignment (rollout slice 0)", () => {
  const aligned = ["design-system", "forms", "lists", "ux-writing", "accessibility-conformance-report", "usability-tasks", "usability-gate", "usability-baseline-report"];

  it("points each aligned doc to docs/ux on its first line", () => {
    const without = aligned.filter((name) => !(alignedDocs[`../../docs/${name}.md`] ?? "").split("\n")[2]?.startsWith("Extended by docs/ux/ (UXS-"));
    expect(without).toEqual([]);
  });

  it("gives the baseline report the programme's extra columns", () => {
    const report = alignedDocs["../../docs/usability-baseline-report.md"] ?? "";
    for (const column of ["Unaided success", "Interactions (median)", "Budget", "SEQ", "Not measured"]) expect(report).toContain(column);
  });

  it("announces the usability check and the supplier demo in What's new", () => {
    expect(whatsNew).toContain("## 2026-11-17");
  });
});
