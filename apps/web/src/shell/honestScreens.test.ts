import { matchPath } from "react-router-dom";
import { build } from "vite";
import { describe, expect, it, vi } from "vitest";
import { loadModules } from "../modules/registry";
import allowlist from "./honestScreensAllowlist.json";
import { plannedRouteFor, plannedRoutes } from "./plannedRoutes";
import { shellNav } from "./shellNav";
import { loadWorkspaceConfig } from "./workspaces/model";

const sources = import.meta.glob<string>(
  ["../modules/**/*.{ts,tsx}", "./**/*.{ts,tsx}", "!../modules/**/*.test.*", "!./**/*.test.*", "!../modules/**/*.d.ts", "!./**/*.d.ts"],
  { query: "?raw", import: "default", eager: true },
);
const demoWorkflow = import.meta.glob<string>("../demo/handlers/workflow.ts", { query: "?raw", import: "default", eager: true });

const SAMPLE_STRINGS = ["Sample data", "Coming soon"];
const SAMPLE_ROWS = ["rows-1m", "outbox.sweep", "workflow.workitem.assigned"];
const SIGNATURE = /function\s+[A-Z]\w*\(\s*\{[\s\S]{0,400}?\}\s*:/g;
const LITERAL_DEFAULT = /=\s*(?:\[\s*\{|\{\s*\w+\s*:\s*\d)/;
const RENDERS_EMPTY = /<StatTile|<EmptyState|\bempty=/;
const FETCHES = /\buse(?:Infinite)?Query\(|\buseLoad\(|\buseInbox\(|\buseMutation\(|\busePagedRows\(|\buseRows\(/;

type Source = Record<string, string>;
type Rule = "sample-string" | "default-rows" | "unfetched-empty" | "sample-literal";

const listed = allowlist.allow as { file: string; rule: Rule; reason: string }[];
const fileOf = (path: string) => path.replace(/^(\.\.\/|\.\/)/, "");
const live = (files: Source) => Object.entries(files).filter(([path]) => !listed.some((a) => path.endsWith(a.file)));

export function findSampleStrings(files: Source): string[] {
  return live(files).flatMap(([path, text]) => SAMPLE_STRINGS.filter((s) => text.includes(s)).map((s) => `${fileOf(path)}: "${s}"`));
}

export function findDefaultRows(files: Source): string[] {
  return live(files).filter(([, text]) => (text.match(SIGNATURE) ?? []).some((signature) => LITERAL_DEFAULT.test(signature))).map(([path]) => fileOf(path));
}

export function findUnfetchedEmptyStates(files: Source): string[] {
  return live(files).filter(([, text]) => RENDERS_EMPTY.test(text) && !FETCHES.test(text)).map(([path]) => fileOf(path));
}

export function findSampleLiterals(texts: Source): string[] {
  return live(texts).flatMap(([path, text]) => SAMPLE_ROWS.filter((s) => text.includes(s)).map((s) => `${fileOf(path)}: ${s}`));
}

export function plannedPathsServed(paths: string[]): string[] {
  return paths.filter((path) => plannedRouteFor(path) !== undefined || plannedRoutes.some((p) => matchPath(path, p.path) !== null));
}

const pageSources = Object.fromEntries(Object.entries(sources).filter(([path]) => /^\.\.\/modules\/(?:admin|workflow)\/[^/]+\.tsx$/.test(path)));

describe("honest screens scan", () => {
  it("lists no sample data or coming soon text in a module or the shell", () => {
    expect(findSampleStrings(sources)).toEqual([]);
  });

  it("holds no literal rows or counts as a default prop", () => {
    expect(findDefaultRows(sources)).toEqual([]);
  });

  it("renders an empty state or tiles only from a page that fetches", () => {
    expect(findUnfetchedEmptyStates(pageSources)).toEqual([]);
  });

  it("keeps the workflow sample rows out of every module and in the demo handler only", () => {
    expect(findSampleLiterals(sources)).toEqual([]);
    const [demo] = Object.values(demoWorkflow);
    for (const literal of SAMPLE_ROWS) expect(demo, literal).toContain(literal);
  });

  it("gives every planned route an issue, a slice and a date that has not passed", () => {
    const today = new Date().toISOString().slice(0, 10);
    expect(plannedRoutes.length).toBeGreaterThan(0);
    const stale = plannedRoutes.filter((row) => row.date < today).map((row) => `${row.path} (${row.issue}, ${row.date})`);
    expect(stale, "move the date in a reviewed change or ship the page").toEqual([]);
  });

  it("serves no planned route from a module and links none from a menu or workspace", () => {
    const modules = loadModules();
    const routes = modules.flatMap((m) => m.routes.map((r) => r.path ?? ""));
    const nav = [...modules.flatMap((m) => m.nav.map((n) => n.to)), ...shellNav.map((n) => n.to)];
    const workspaces = loadWorkspaceConfig().workspaces.flatMap((w) => w.entries);
    expect(plannedPathsServed(routes.filter((route) => route !== ""))).toEqual([]);
    expect(plannedPathsServed([...nav, ...workspaces])).toEqual([]);
  });

  it("parses planned routes strictly", () => {
    expect(plannedRouteFor("/admin/audit")?.issue).toBe("B-23");
    expect(plannedRouteFor("/admin/audit/42")?.issue).toBe("B-23");
    expect(plannedRouteFor("/admin/auditor")).toBeUndefined();
    expect(plannedRouteFor("/admin/sequences")).toBeUndefined();
    expect(plannedRouteFor("")).toBeUndefined();
  });
});

describe("honest screens scan negative controls", () => {
  it("flags sample strings", () => {
    expect(findSampleStrings({ "../modules/x/A.tsx": '<Badge>Sample data</Badge>', "../modules/x/B.tsx": "Coming soon" })).toHaveLength(2);
    expect(findSampleStrings({ "../modules/x/C.tsx": "<Badge>Shipped</Badge>" })).toEqual([]);
  });

  it("flags a default prop that holds rows or counts", () => {
    const rows = 'export function JobMonitorPage({ runs = [{ code: "a" }] }: { runs?: Run[] }) {';
    const counts = "export function Home({ counts = { running: 1, failed: 0 } }: { counts?: Counts }) {";
    const empty = "export function Page({ items = [] }: { items?: Row[] }) { return cond ? { a: 1 } : null; }";
    expect(findDefaultRows({ "../modules/x/A.tsx": rows, "../modules/x/B.tsx": counts })).toHaveLength(2);
    expect(findDefaultRows({ "../modules/x/C.tsx": empty })).toEqual([]);
  });

  it("flags an empty state or tiles with no fetch behind them", () => {
    expect(findUnfetchedEmptyStates({ "../modules/admin/A.tsx": "<EmptyState title='x' />" })).toHaveLength(1);
    expect(findUnfetchedEmptyStates({ "../modules/admin/B.tsx": "<StatTile />" })).toHaveLength(1);
    expect(findUnfetchedEmptyStates({ "../modules/admin/C.tsx": "const q = useInfiniteQuery({}); <DataTable empty={x} />" })).toEqual([]);
  });

  it("flags sample row literals", () => {
    expect(findSampleLiterals({ "bundle.js": 'code:"rows-1m"' })).toEqual(["bundle.js: rows-1m"]);
    expect(findSampleLiterals({ "bundle.js": "nothing here" })).toEqual([]);
  });

  it("flags a served planned path", () => {
    expect(plannedPathsServed(["/admin/audit", "/admin/audit/:id", "/admin/sequences"])).toEqual(["/admin/audit", "/admin/audit/:id"]);
  });

  it("keeps an allowlist entry only while its file still exists", () => {
    expect(listed.filter((entry) => !Object.keys(sources).some((path) => path.endsWith(entry.file)))).toEqual([]);
  });
});

describe("production build", () => {
  it("holds none of the workflow sample rows", async () => {
    vi.stubEnv("NODE_ENV", "production");
    let outputs: { output: ({ type: string; code?: string; source?: string | Uint8Array })[] }[];
    try {
      const built = await build({ mode: "production", logLevel: "silent", build: { write: false } });
      outputs = (Array.isArray(built) ? built : [built]) as unknown as typeof outputs;
    } finally {
      vi.unstubAllEnvs();
    }
    const chunks = outputs.flatMap((o) => o.output.map((file, index) => [`output ${index}`, file.code ?? (typeof file.source === "string" ? file.source : "")] as const));
    expect(chunks.map(([, text]) => text).join("\n"), "the build holds the app").toContain("No number sequences yet");
    expect(findSampleLiterals(Object.fromEntries(chunks))).toEqual([]);
  }, 600_000);
});
