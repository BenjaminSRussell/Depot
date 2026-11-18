/**
 * Honest screens (UXS-01, docs/ux/patterns.md): a screen shows only what it got from a real data source.
 *
 * 1. A page that renders an empty state, a stat tile or a progress value makes a real fetch call: a fetch hook, fetch
 *    itself or a function imported (as a value, not a type) from an API client. A component with no props that shows
 *    one must fetch itself; a sibling's fetch does not count for it.
 * 2. No page component has a default prop holding rows, counts or statuses, written inline or held in a constant.
 * 3. No page without a fetch renders a hard-coded list of rows (a module-level array mapped into <li> or <tr>, or
 *    passed as rows=).
 * 4. "Sample data" and "Coming soon" render nowhere outside src/demo/.
 * 5. Every route in shell/plannedRoutes.json renders the planned page and has an issue, a slice and a target date that
 *    has not passed. The shell offers none of them: not in the sidebar, a workspace, All features, the home page, the
 *    command palette or an admin home card.
 * 6. Source scan for the production build (not a build): the workflow sample rows and the demo company appear in no
 *    source outside src/demo/, and src/demo/ is reached only through imports behind the demo-mode check. The build
 *    itself is checked by the production-build grep in apps/web/docs/design-system.md, section 8.
 *
 * Every detector has fixtures below, including the ways around it that review found (mutation tests). The allowlists
 * may only shrink. To leave one, fetch the data or delete the fake.
 */
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { createElement as h, isValidElement } from "react";
import { MemoryRouter, Route, Routes } from "react-router-dom";
import { parseAst } from "vite";
import { afterEach, beforeEach, vi } from "vitest";
import { AdminHome, adminCards } from "../modules/admin/pages";
import { loadModules } from "../modules/registry";
import { AllFeaturesPage } from "./pages/AllFeaturesPage";
import { HomePage } from "./pages/HomePage";
import { PlannedPage, plannedRoutes, withoutPlannedNav } from "./pages/PlannedPage";
import { DELIVERY_REF, SPEC_REF } from "./plainLanguageRules";
import { Shell } from "./Shell";
import { auditByTo, loadWorkspaceConfig, resolveNavEntries, resolveWorkspaces } from "./workspaces/model";

/** Components without props that show a state without fetching, as `modules/<path>:<Component>`, with the reason. */
const NO_FETCH_ALLOWLIST: Record<string, string> = {
  "modules/hr/PerformancePages.tsx:NoLinkedWorker": "Rendered by its parent only after the worker lookup came back empty.",
};

/** Pages that render a module-level list without fetching, as `modules/<path>:<constant>`, with the reason. */
const STATIC_LIST_ALLOWLIST: Record<string, string> = {};

/** Page components whose default props hold rows or counts, as `modules/<path>:<Component>.<prop>`, with the reason. */
const DEFAULT_PROP_ALLOWLIST: Record<string, string> = {
  "modules/mobile-flows/ExceptionsPanel.tsx:ExceptionsPanel.overrideReasons": "The reasons a supervisor can pick from, not data shown as fact.",
  "modules/warehouse/pages.tsx:DirectiveSimulatorPage.initial": "An editable example input for the simulator; the result comes from the warehouse service.",
};

const pageSources = import.meta.glob<string>(["../modules/**/*.tsx", "!**/*.test.tsx", "!**/testing/**", "!**/*TestKit.tsx"], {
  query: "?raw",
  import: "default",
  eager: true,
});

const userSources = import.meta.glob<string>(["../modules/**/*.{ts,tsx}", "./**/*.{ts,tsx}", "!**/*.test.{ts,tsx}", "!**/testing/**", "../main.tsx"], {
  query: "?raw",
  import: "default",
  eager: true,
});

const rel = (path: string) => path.replace(/^\.\.\//, "").replace(/^\.\//, "shell/");

type Node = { type?: unknown; [key: string]: unknown };
const isNode = (value: unknown): value is Node => typeof value === "object" && value !== null && typeof (value as Node).type === "string";

function children(node: Node): Node[] {
  const out: Node[] = [];
  for (const [key, value] of Object.entries(node)) {
    if (key === "type" || key === "start" || key === "end") continue;
    if (Array.isArray(value)) for (const v of value) if (isNode(v)) out.push(v);
    if (isNode(value)) out.push(value);
  }
  return out;
}

function walk(node: Node, visit: (node: Node) => void) {
  visit(node);
  for (const child of children(node)) walk(child, visit);
}

function some(node: Node, test: (node: Node) => boolean): boolean {
  if (test(node)) return true;
  return children(node).some((child) => some(child, test));
}

const name = (node: unknown): string | undefined => (isNode(node) && typeof node.name === "string" ? node.name : undefined);

/** Calls that fetch whatever module they are imported from. */
const FETCH_CALLS = new Set(["useQuery", "useQueries", "useInfiniteQuery", "useSuspenseQuery", "useResource", "useLoad", "usePagedRows", "usePaged", "useMutation", "fetch", "apiFetch", "requestUrl", "request"]);
/** Module paths of the API clients: `./api`, `./api/client`, `../x/api/...`, `../../shell/api/http`, `./client`. */
const API_MODULE = /(^|\/)api(\/|$)|(^|\/)client$/;
const STATE_ELEMENTS = new Set(["EmptyState", "StatTile", "progress"]);
const PROMPT = /^(Choose|Select|Enter|Pick) /;

export interface PageFacts {
  /** The file makes at least one real fetch call. */
  fetches: boolean;
  /** The file renders an empty state, a stat tile, a progress value or a table with an empty message. */
  showsState: boolean;
  /** Components with no props that show such a state without fetching themselves. */
  paramlessWithoutFetch: string[];
  /** `Component.prop` defaults holding an array, or a non-empty object, inline or through a module constant. */
  literalDefaults: string[];
  /** Module-level array constants the file renders as list items, table rows or `rows=`. */
  staticLists: string[];
}

const parse = (source: string, file: string) => parseAst(source, { lang: file.endsWith(".tsx") ? "tsx" : "ts" }, file) as unknown as Node;

/** `{ local: from }` for the value imports of a file: `import type` and `import { type X }` bring nothing that can run. */
function valueImports(program: Node): Map<string, string> {
  const out = new Map<string, string>();
  for (const decl of (program.body as Node[]) ?? []) {
    if (decl.type !== "ImportDeclaration" || decl.importKind === "type") continue;
    for (const spec of decl.specifiers as Node[]) if (spec.importKind !== "type") out.set(String(name(spec.local)), String((decl.source as Node).value));
  }
  return out;
}

/** The name a call is made through: `f()` gives f, `api.f()` gives api. */
function calledName(node: Node): string | undefined {
  if (node.type !== "CallExpression") return undefined;
  const callee = node.callee as Node;
  return callee.type === "Identifier" ? name(callee) : callee.type === "MemberExpression" ? name(callee.object) : undefined;
}

/**
 * Decides whether an import specifier of a file leads to a module that fetches: an API client by its path, or (for the
 * real sources) a file the project-wide pass below found to fetch, such as a module's own `useInvoices` hook.
 */
export type FetchingImport = (from: string, file: string) => boolean;
const apiPathOnly: FetchingImport = (from) => API_MODULE.test(from);

export function analyzePage(source: string, file: string, fetchingImport: FetchingImport = apiPathOnly): PageFacts {
  const program = parse(source, file);
  const body = (program.body as Node[]) ?? [];

  const imported = valueImports(program);
  const fetchingBindings = new Set([...imported].filter(([, from]) => fetchingImport(from, file)).map(([local]) => local));
  const directFetch = (node: Node) => {
    const called = calledName(node);
    return called !== undefined && (FETCH_CALLS.has(called) || fetchingBindings.has(called));
  };
  // Module-level functions that fetch, directly or through another one: a component that calls or passes one (such as
  // `useRows={useRows}` on ListPage) gets its data from a fetch.
  const topLevel = new Map<string, Node>();
  for (const stmt of body) {
    const decl = stmt.type === "ExportNamedDeclaration" && isNode(stmt.declaration) ? stmt.declaration : stmt;
    if (decl.type === "FunctionDeclaration" && name(decl.id)) topLevel.set(String(name(decl.id)), decl);
    if (decl.type === "VariableDeclaration") for (const d of decl.declarations as Node[]) if (name(d.id) && isNode(d.init)) topLevel.set(String(name(d.id)), d.init);
  }
  const localFetchers = new Set<string>();
  for (let grew = true; grew; ) {
    grew = false;
    for (const [local, node] of topLevel) {
      if (localFetchers.has(local)) continue;
      if (some(node, (n) => directFetch(n) || (n.type === "Identifier" && localFetchers.has(String(name(n)))))) {
        localFetchers.add(local);
        grew = true;
      }
    }
  }
  // A real fetch call, or a reference to something that fetches (a local loader passed as `useRows`, an API function
  // handed to a calculation hook). Imports themselves sit outside every component, so an unused import never counts.
  const isFetchCall = (node: Node) =>
    directFetch(node) || (node.type === "Identifier" && (localFetchers.has(String(name(node))) || fetchingBindings.has(String(name(node)))));
  // An empty state that asks for input ("Choose a warehouse") claims nothing about data, so it needs no fetch.
  const prompt = (node: Node) =>
    ((node.attributes as Node[]) ?? []).some((a) => name(a.name) === "title" && isNode(a.value) && typeof a.value.value === "string" && PROMPT.test(a.value.value));
  const isState = (node: Node) =>
    (node.type === "JSXOpeningElement" && STATE_ELEMENTS.has(String(name(node.name))) && !prompt(node)) || (node.type === "JSXAttribute" && name(node.name) === "empty");

  // Module-level constants by name, for defaults and lists held in a constant.
  const constants = new Map<string, Node>();
  for (const stmt of body) {
    const decl = stmt.type === "ExportNamedDeclaration" && isNode(stmt.declaration) ? stmt.declaration : stmt;
    if (decl.type !== "VariableDeclaration") continue;
    for (const d of decl.declarations as Node[]) if (name(d.id) && isNode(d.init)) constants.set(String(name(d.id)), d.init);
  }
  const resolve = (node: Node): Node => (node.type === "Identifier" && constants.has(String(name(node))) ? (constants.get(String(name(node))) as Node) : node);
  const holdsData = (node: Node) => {
    const value = resolve(node);
    return value.type === "ArrayExpression" || (value.type === "ObjectExpression" && (value.properties as unknown[]).length > 0);
  };

  const components: { name: string; fn: Node }[] = [];
  walk(program, (node) => {
    if (node.type === "FunctionDeclaration" && /^[A-Z]/.test(String(name(node.id)))) components.push({ name: String(name(node.id)), fn: node });
    if (node.type === "VariableDeclarator" && /^[A-Z]/.test(String(name(node.id))) && isNode(node.init) && /Function/.test(String(node.init.type)))
      components.push({ name: String(name(node.id)), fn: node.init });
  });

  // Local components that show a state without fetching it themselves, directly or by rendering another one: the old
  // `SequencePage` rendered an `EmptyPage` helper that rendered the EmptyState. A child that fetches answers for itself.
  const showsStateLocally = new Set<string>();
  const rendersState = (node: Node) =>
    isState(node) || (node.type === "JSXOpeningElement" && showsStateLocally.has(String(name(node.name))));
  for (let grew = true; grew; ) {
    grew = false;
    for (const { name: component, fn } of components) {
      if (!showsStateLocally.has(component) && some(fn, rendersState) && !some(fn, isFetchCall)) {
        showsStateLocally.add(component);
        grew = true;
      }
    }
  }

  const literalDefaults: string[] = [];
  const paramlessWithoutFetch: string[] = [];
  for (const { name: component, fn } of components) {
    const params = (fn.params as Node[]) ?? [];
    const [props] = params;
    if (isNode(props) && props.type === "ObjectPattern") {
      for (const prop of props.properties as Node[]) {
        const value = prop.value as Node | undefined;
        if (value?.type === "AssignmentPattern" && holdsData(value.right as Node)) literalDefaults.push(`${component}.${String(name(prop.key))}`);
      }
    }
    // A component with props may show what its parent fetched; one without props has nowhere else to get it.
    if (params.length === 0 && some(fn, rendersState) && !some(fn, isFetchCall)) paramlessWithoutFetch.push(component);
  }

  // A module-level array of two or more literal rows, mapped into <li> or <tr> or passed as rows=. A list of links
  // (every row has `to` or `href`) is navigation, not data.
  const isLink = (row: Node) => ((row.properties as Node[]) ?? []).some((p) => ["to", "href"].includes(String(name(p.key))));
  const rowsConstant = (node: Node): string | undefined => {
    if (node.type !== "Identifier") return undefined;
    const value = constants.get(String(name(node)));
    const elements = value?.type === "ArrayExpression" ? (value.elements as Node[]) : [];
    const literal = elements.length >= 2 && elements.every((e) => isNode(e) && (e.type === "ObjectExpression" || (e.type === "Literal" && typeof e.value === "string")));
    const links = elements.every((e) => isNode(e) && e.type === "ObjectExpression" && isLink(e));
    return literal && !links ? String(name(node)) : undefined;
  };
  const listItem = (node: Node) => node.type === "JSXOpeningElement" && ["li", "tr"].includes(String(name(node.name)));
  const staticLists = new Set<string>();
  walk(program, (node) => {
    if (node.type === "JSXAttribute" && name(node.name) === "rows" && isNode(node.value) && isNode(node.value.expression)) {
      const hit = rowsConstant(node.value.expression);
      if (hit) staticLists.add(hit);
    }
    if (node.type !== "CallExpression" || (node.callee as Node).type !== "MemberExpression") return;
    const callee = node.callee as Node;
    if (name(callee.property) !== "map" && name(callee.property) !== "filter") return;
    // Follow `X.filter(...).map(...)` back to X.
    let target = callee.object as Node;
    while (target.type === "CallExpression" && (target.callee as Node).type === "MemberExpression") target = (target.callee as Node).object as Node;
    const hit = rowsConstant(target);
    const [callback] = (node.arguments as Node[]) ?? [];
    if (hit && isNode(callback) && some(callback, listItem)) staticLists.add(hit);
  });

  return {
    fetches: body.some((stmt) => stmt.type !== "ImportDeclaration" && some(stmt, isFetchCall)),
    showsState: some(program, isState),
    paramlessWithoutFetch,
    literalDefaults,
    staticLists: [...staticLists],
  };
}

// Project-wide: which source files fetch, directly or through an import of one that does (a module's own hooks and
// loaders). Paths are glob keys, relative to this folder.
const canonical = (key: string) => (key.startsWith("../") ? `src/${key.slice(3)}` : `src/shell/${key.slice(2)}`);
const keyByCanonical = new Map(Object.keys(userSources).map((key) => [canonical(key), key]));

function resolveImport(from: string, file: string): string | undefined {
  if (!from.startsWith(".")) return undefined;
  const dir = canonical(file).split("/").slice(0, -1);
  for (const segment of from.split("/")) {
    if (segment === "..") dir.pop();
    else if (segment !== ".") dir.push(segment);
  }
  const base = dir.join("/");
  for (const ext of ["", ".ts", ".tsx", "/index.ts", "/index.tsx"]) {
    const key = keyByCanonical.get(base + ext);
    if (key) return key;
  }
  return undefined;
}

const programs = new Map(Object.entries(userSources).map(([key, source]) => [key, parse(source, key)]));
const fetchingFiles = new Set<string>();
const leadsToFetch: FetchingImport = (from, file) => API_MODULE.test(from) || fetchingFiles.has(resolveImport(from, file) ?? "");
for (let grew = true; grew; ) {
  grew = false;
  for (const [key, program] of programs) {
    if (fetchingFiles.has(key)) continue;
    const imported = valueImports(program);
    const bindings = new Set([...imported].filter(([, from]) => leadsToFetch(from, key)).map(([local]) => local));
    const reexports = ((program.body as Node[]) ?? []).some((s) => (s.type === "ExportNamedDeclaration" || s.type === "ExportAllDeclaration") && isNode(s.source) && leadsToFetch(String(s.source.value), key));
    if (reexports || some(program, (n) => { const called = calledName(n); return called !== undefined && (FETCH_CALLS.has(called) || bindings.has(called)); })) {
      fetchingFiles.add(key);
      grew = true;
    }
  }
}

const facts = Object.fromEntries(Object.entries(pageSources).map(([path, source]) => [rel(path), analyzePage(source, path, leadsToFetch)]));

describe("honest screens: the detectors catch the ways around them", () => {
  const page = (code: string) => analyzePage(code, "fixture.tsx");

  it("counts only real fetch calls, not type imports, comments or unused imports", () => {
    expect(page('import { useQuery } from "@tanstack/react-query";\nexport function P() { const q = useQuery({}); return <EmptyState title="x" />; }').fetches).toBe(true);
    expect(page('import { listRuns } from "./api/client";\nexport function P() { void listRuns(); return null; }').fetches).toBe(true);
    expect(page('import * as api from "../api";\nexport function P() { void api.listRuns(); return null; }').fetches).toBe(true);
    // Mutation m5: a type-only import from the API client.
    expect(page('import type { Run } from "./api/client";\nexport function P() { return <EmptyState title="No runs" />; }').fetches).toBe(false);
    expect(page('import { type Run } from "./api/client";\nexport function P() { return <EmptyState title="No runs" />; }').fetches).toBe(false);
    // Mutation m14: useQuery only mentioned.
    expect(page('// uses useQuery later\nimport { useQuery } from "@tanstack/react-query";\nexport function P() { return <StatTile label="Runs" value={1} />; }').fetches).toBe(false);
  });

  it("flags a component without props that shows a state without fetching, even when a sibling fetches", () => {
    const code = [
      'import { useLoad } from "./useLoad";',
      'import { calcQuota } from "./api/client";',
      "function useRows() { return useLoad(() => fetch('/x')); }",
      "export function Real() { const s = useLoad(() => fetch('/x')); return <DataTable rows={s} empty={{ title: 'None' }} />; }",
      "export function Listed() { return <ListPage useRows={useRows} empty={{ title: 'None' }} />; }",
      "export function Calculator() { const run = useCalculation(calcQuota); return <DataTable rows={run} empty={{ title: 'None' }} />; }",
      // Mutation m6, the old SequencePage: a static empty state, here through a local helper.
      'function EmptyPage({ title }) { return <EmptyState title={title} />; }',
      'export function Fake() { return <EmptyPage title="No sequences set up yet" />; }',
      'export function Tiles() { return <StatTile label="Running jobs" value={1} />; }',
      "export function Child({ rows }) { return <DataTable rows={rows} empty={{ title: 'None' }} />; }",
      'export function Prompt() { return <EmptyState title="Choose a warehouse" />; }',
    ].join("\n");
    expect(page(code).paramlessWithoutFetch).toEqual(["Fake", "Tiles"]);
  });

  it("flags defaults holding rows or counts, inline or through a module constant", () => {
    const code = [
      'const SAMPLE_RUNS = [{ code: "rows-1m" }];',
      "const NO_ERRORS = {};",
      "export function AuditViewer({ items = [] }) { return null; }",
      "export const Home = ({ counts = { running: 1 }, title = 'x', errors = {} }) => null;",
      // Mutation m2: the rows held in a named constant.
      "export function JobsPage({ runs = SAMPLE_RUNS, errors = NO_ERRORS }) { return null; }",
      "function helper({ rows = [] }) { return rows; }",
    ].join("\n");
    expect(page(code).literalDefaults).toEqual(["AuditViewer.items", "Home.counts", "JobsPage.runs"]);
  });

  it("flags a hard-coded list rendered as list items, table rows or rows=", () => {
    // Mutations m3 and m13: the old designer steps and template codes.
    const steps = 'const steps = [{ name: "Manager role" }, { name: "Director user" }];\nexport function Designer() { return <ol>{steps.map((s) => <li key={s.name}>{s.name}</li>)}</ol>; }';
    const codes = 'const shipped = ["workflow.workitem.assigned", "batch.run.failed"];\nexport function Templates() { return <ul>{shipped.filter(Boolean).map((c) => <li key={c}>{c}</li>)}</ul>; }';
    const table = 'const runs = [{ code: "a" }, { code: "b" }];\nexport function Jobs() { return <DataTable rows={runs} />; }';
    const options = 'const kinds = [{ value: "a" }, { value: "b" }];\nexport function Picker() { return <select>{kinds.map((k) => <option key={k.value}>{k.value}</option>)}</select>; }';
    const links = 'const cards = [{ to: "/a", title: "A" }, { to: "/b", title: "B" }];\nexport function Home() { return <ul>{cards.map((c) => <li key={c.to}>{c.title}</li>)}</ul>; }';
    expect([steps, codes, table, options, links].map((code) => page(code).staticLists)).toEqual([["steps"], ["shipped"], ["runs"], [], []]);
  });
});

describe("honest screens: source scan", () => {
  it("covers the module pages", () => {
    expect(Object.keys(pageSources).length).toBeGreaterThan(500);
  });

  it("shows an empty state, count or progress only in a component that fetches or is handed the data", () => {
    const offenders = Object.entries(facts)
      .flatMap(([file, f]) => f.paramlessWithoutFetch.map((c) => `${file}:${c}`))
      .filter((hit) => !(hit in NO_FETCH_ALLOWLIST));
    expect(offenders, "fetch in the component that shows the state, pass it the fetched data, or delete the page").toEqual([]);
  });

  it("gives no page component a default prop holding rows, counts or statuses", () => {
    const offenders = Object.entries(facts)
      .flatMap(([file, f]) => f.literalDefaults.map((hit) => `${file}:${hit}`))
      .filter((hit) => !(hit in DEFAULT_PROP_ALLOWLIST));
    expect(offenders, "pass real data from a fetch instead of a default").toEqual([]);
  });

  it("renders no hard-coded list of rows on a page that does not fetch", () => {
    const offenders = Object.entries(facts)
      .filter(([, f]) => !f.fetches)
      .flatMap(([file, f]) => f.staticLists.map((list) => `${file}:${list}`))
      .filter((hit) => !(hit in STATIC_LIST_ALLOWLIST));
    expect(offenders, "list what a fetch returned, or allowlist genuinely static content with a reason").toEqual([]);
  });

  it('renders "Sample data" and "Coming soon" only in the demo', () => {
    const offenders = Object.entries(userSources)
      .filter(([, source]) => /sample data|coming soon/i.test(source))
      .map(([path]) => rel(path));
    expect(offenders).toEqual([]);
  });

  it("allowlists only files that exist and still need it", () => {
    for (const entry of Object.keys(NO_FETCH_ALLOWLIST)) {
      const [file, component] = entry.split(":");
      expect(facts[file ?? ""]?.paramlessWithoutFetch ?? [], `${entry} no longer needs its allowlist entry`).toContain(component);
    }
    for (const entry of Object.keys(DEFAULT_PROP_ALLOWLIST)) {
      const [file, prop] = entry.split(":");
      expect(facts[file ?? ""]?.literalDefaults ?? [], `${entry} no longer needs its allowlist entry`).toContain(prop);
    }
    for (const entry of Object.keys(STATIC_LIST_ALLOWLIST)) {
      const [file, list] = entry.split(":");
      expect(facts[file ?? ""]?.staticLists ?? [], `${entry} no longer needs its allowlist entry`).toContain(list);
    }
  });
});

describe("honest screens: planned routes", () => {
  const today = new Date().toISOString().slice(0, 10);
  const modules = loadModules();
  const routes = modules.flatMap((m) => m.routes.map((r) => ({ module: m.id, path: r.path, element: r.element })));
  const plannedPaths = new Set(plannedRoutes.map((r) => r.path));
  const allNavDuties = [...new Set(modules.flatMap((m) => m.nav.flatMap((n) => (n.duty ? [n.duty] : []))))];

  beforeEach(() => {
    vi.stubGlobal("fetch", vi.fn(async () => new Response(JSON.stringify({ items: [] }), { status: 200, headers: { "Content-Type": "application/json" } })));
  });
  afterEach(() => vi.unstubAllGlobals());

  it("dates, sources and explains every planned route", () => {
    expect(plannedRoutes.length).toBeGreaterThan(0);
    expect(plannedPaths.size).toBe(plannedRoutes.length);
    for (const r of plannedRoutes) {
      expect(r.path, r.path).toMatch(/^\/[a-z]/);
      expect(r.issue, r.path).toBeGreaterThan(0);
      expect(r.backlog, r.path).toMatch(/^B-\d+[a-z]?$/);
      expect(Number.isInteger(r.slice), r.path).toBe(true);
      expect(r.reason.trim(), r.path).not.toBe("");
      expect(r.targetDate, r.path).toMatch(/^\d{4}-\d{2}-\d{2}$/);
      expect(Number.isNaN(Date.parse(r.targetDate)), r.path).toBe(false);
      expect(r.targetDate >= today, `${r.path} was due ${r.targetDate}: ship it and remove the entry, or move the date`).toBe(true);
      // The description is shown to people (apps/web/docs/ux-writing.md).
      expect(r.description, r.path).toMatch(/^[A-Z].*\.$/);
      expect(SPEC_REF.test(r.description) || DELIVERY_REF.test(r.description), r.path).toBe(false);
    }
  });

  it("renders the planned page on every planned route, and only there", () => {
    const rendersPlanned = routes.filter((r) => isValidElement(r.element) && r.element.type === PlannedPage);
    expect(rendersPlanned.map((r) => r.path).sort()).toEqual([...plannedPaths].sort());
    for (const r of rendersPlanned) expect((r.element as { props: { path?: string } }).props.path).toBe(r.path);
  });

  it("lists no planned route in a shipped workspace, nor in one a tenant adds", () => {
    const config = loadWorkspaceConfig();
    expect(config.workspaces.flatMap((w) => w.entries.filter((to) => plannedPaths.has(to)).map((to) => `${w.id} ${to}`))).toEqual([]);
    const override = loadWorkspaceConfig({ workspaces: [{ id: "custom", label: "Custom", persona: "Custom role", description: "Added by a tenant.", entries: [...plannedPaths, "/admin/sequences"] }] });
    const entries = resolveNavEntries(withoutPlannedNav(modules), override.areas);
    const custom = resolveWorkspaces(override, entries).find((w) => w.id === "custom");
    expect(custom?.entries.map((e) => e.to)).toEqual(["/admin/sequences"]);
  });

  it("offers no planned route in the sidebar, All features, the home page or the command palette", async () => {
    const user = userEvent.setup();
    const shell = (path: string) =>
      render(
        h(
          MemoryRouter,
          { initialEntries: [path] },
          h(
            Routes,
            null,
            h(
              Route,
              { element: h(Shell, { modules, duties: allNavDuties, features: {} }) },
              h(Route, { path: "/", element: h(HomePage) }),
              h(Route, { path: "/features", element: h(AllFeaturesPage) }),
            ),
          ),
        ),
      );
    const plannedLinks = () => [...document.querySelectorAll("a[href]")].map((a) => a.getAttribute("href")).filter((href) => plannedPaths.has(href ?? ""));

    const features = shell("/features");
    expect(screen.getAllByRole("link").length).toBeGreaterThan(200);
    expect(plannedLinks(), "sidebar and All features").toEqual([]);
    features.unmount();

    shell("/");
    expect(plannedLinks(), "sidebar and home").toEqual([]);
    await user.keyboard("{Control>}k{/Control}");
    const options = within(screen.getByRole("dialog", { name: "Search" }))
      .getAllByRole("option")
      .map((o) => o.getAttribute("aria-label"));
    expect(options.length).toBeGreaterThan(200);
    const plannedOptions = modules.flatMap((m) =>
      m.nav.filter((n) => plannedPaths.has(n.to)).map((n) => `${auditByTo.get(n.to)?.taskLabel ?? n.label}, ${m.title}`),
    );
    expect(plannedOptions.length).toBe(plannedPaths.size);
    expect(options.filter((o) => plannedOptions.includes(o ?? "")), "command palette").toEqual([]);
  }, 60_000);

  it("gives no planned route a card on the admin home", () => {
    expect(adminCards.filter((card) => plannedPaths.has(card.to)).length, "the fixture must hold planned cards to prove the filter").toBeGreaterThan(0);
    render(h(MemoryRouter, null, h(AdminHome)));
    const hrefs = within(screen.getByRole("list", { name: "Admin pages" }))
      .getAllByRole("link")
      .map((a) => a.getAttribute("href"));
    expect(hrefs.length).toBe(adminCards.length - adminCards.filter((card) => plannedPaths.has(card.to)).length);
    expect(hrefs.filter((href) => plannedPaths.has(href ?? ""))).toEqual([]);
  });
});

describe("honest screens: production-build source scan", () => {
  // Rows the job monitor once showed as defaults in every build (now src/demo/handlers/workflow.ts), and the demo company.
  const markers = ["rows-1m", "400000 / 1000000", "workflow.workitem.escalated", "Cedar Ridge", "cedarridgepumps.example"];

  it("keeps the workflow sample rows and the demo company out of every source outside src/demo/", () => {
    const offenders = Object.entries(userSources).flatMap(([path, source]) => markers.filter((m) => source.includes(m)).map((m) => `${rel(path)}: ${m}`));
    expect(offenders).toEqual([]);
  });

  it("reaches src/demo/ only through dynamic imports behind the demo-mode check", () => {
    const outside = Object.entries(userSources).filter(([path]) => !path.startsWith("../demo/"));
    const staticDemo = outside.filter(([, source]) => /from ["'](?:\.\.\/)+demo\/|from ["']\.\/demo\//.test(source)).map(([path]) => rel(path));
    expect(staticDemo, "import src/demo/ dynamically, behind import.meta.env.MODE === \"demo\"").toEqual([]);
    const dynamic = outside.flatMap(([path, source]) =>
      source
        .split("\n")
        .filter((line) => /import\(["'](\.\.\/)*\.?\/?demo\//.test(line))
        .map((line) => ({ file: rel(path), line })),
    );
    expect(dynamic.map((d) => d.file).sort()).toEqual(["main.tsx", "shell/pages/HomePage.tsx"]);
    const main = userSources["../main.tsx"] ?? "";
    expect(main).toContain('const demo = import.meta.env.MODE === "demo";');
    for (const { file, line } of dynamic) expect(line, file).toMatch(/demo \?|MODE === "demo" \?/);
  });
});
