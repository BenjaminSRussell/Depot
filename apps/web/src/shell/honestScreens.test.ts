/**
 * Honest screens (UXS-01, docs/ux/patterns.md): a screen shows only what it got from a real data source.
 *
 * 1. A page that renders an empty state, a stat tile or a progress value also fetches (a fetch hook, a module API
 *    client or fetch itself), so the empty message or number came from somewhere.
 * 2. No page component has a default prop holding literal rows, counts or statuses.
 * 3. "Sample data" and "Coming soon" render nowhere outside src/demo/.
 * 4. Every route in shell/plannedRoutes.json renders the planned page, has an issue, a slice and a target date that has
 *    not passed, and no workspace or admin home card links it.
 * 5. The workflow sample rows exist only under src/demo/, which only the demo build loads.
 *
 * The allowlists below may only shrink. To leave one, fetch the data or delete the fake.
 */
import { parseAst } from "vite";
import { isValidElement } from "react";
import { loadModules } from "../modules/registry";
import { adminCards } from "../modules/admin/pages";
import { PlannedPage, plannedRoutes } from "../modules/admin/PlannedPage";
import { DELIVERY_REF, SPEC_REF } from "./plainLanguageRules";
import { loadWorkspaceConfig } from "./workspaces/model";

/** Pages that render an empty state or a number without fetching, as `modules/<path>`, with the reason. */
const NO_FETCH_ALLOWLIST: Record<string, string> = {
  "modules/mobile-flows/FlowSimulator.tsx": "Simulates a device flow in the browser from the flow definition it is given; it shows no stored data.",
};

/** Says a page isn't available yet. It is the honest state itself, so it has no data to fetch. */
const PLANNED_PAGE = "modules/admin/PlannedPage.tsx";

/** Files outside src/demo/ that import from it statically, as a path from src/, with the reason. */
const STATIC_DEMO_IMPORT_ALLOWLIST: Record<string, string> = {
  "shell/pages/HomePage.tsx":
    "Renders DemoTour only when MODE is demo, but the static import still puts the tour data in the production bundle; it should load DemoTour with a dynamic import, as main.tsx does for the banner.",
};

/** Page components whose default props hold literals, as `modules/<path>:<Component>.<prop>`, with the reason. */
const DEFAULT_PROP_ALLOWLIST: Record<string, string> = {};

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

const SHOWS_STATE = /<EmptyState\b|\bempty=\{|<StatTile\b|<progress\b/;
const FETCHES =
  /\buse(Query|Queries|InfiniteQuery|Resource|Load|PagedRows|Paged|Rows|Mutation)\b|\bload=\{|from "[^"]*\/api[/"]|from "\.\/api|\bapiFetch\b|\bfetch\(|\brequestUrl\b|\bdefaultFetch\b/;

type Node = { type?: unknown; [key: string]: unknown };
const isNode = (value: unknown): value is Node => typeof value === "object" && value !== null;

/**
 * `Component.prop` for every capitalized function whose destructured props default to an array literal (even an empty
 * one: `items = []` guarantees an empty table) or a non-empty object literal. `{}` stays allowed: no errors, no filters.
 */
export function literalDefaultProps(source: string, file: string): string[] {
  const found: string[] = [];
  const check = (name: string, fn: Node) => {
    const [props] = (fn.params as Node[] | undefined) ?? [];
    if (!/^[A-Z]/.test(name) || !isNode(props) || props.type !== "ObjectPattern") return;
    for (const prop of props.properties as Node[]) {
      const value = prop.value as Node | undefined;
      if (value?.type !== "AssignmentPattern") continue;
      const right = value.right as Node;
      const literal = right.type === "ArrayExpression" || (right.type === "ObjectExpression" && (right.properties as unknown[]).length > 0);
      if (literal) found.push(`${name}.${String((prop.key as Node).name)}`);
    }
  };
  const visit = (node: unknown) => {
    if (Array.isArray(node)) return node.forEach(visit);
    if (!isNode(node)) return;
    if (node.type === "FunctionDeclaration" && isNode(node.id)) check(String(node.id.name), node);
    if (node.type === "VariableDeclarator" && isNode(node.id) && isNode(node.init) && /Function/.test(String(node.init.type))) check(String(node.id.name), node.init);
    for (const [key, child] of Object.entries(node)) if (key !== "type") visit(child);
  };
  visit(parseAst(source, { lang: file.endsWith(".tsx") ? "tsx" : "ts" }, file));
  return found;
}

describe("honest screens: source scan", () => {
  it("covers the module pages", () => {
    expect(Object.keys(pageSources).length).toBeGreaterThan(500);
  });

  it("shows an empty state, count or progress only on a page that fetches", () => {
    const offenders = Object.entries(pageSources)
      .filter(([, source]) => SHOWS_STATE.test(source) && !FETCHES.test(source))
      .map(([path]) => rel(path))
      .filter((file) => file !== PLANNED_PAGE && !(file in NO_FETCH_ALLOWLIST));
    expect(offenders, "fetch the data this page shows, or delete the page").toEqual([]);
  });

  it("gives no page component a default prop holding literal rows, counts or statuses", () => {
    const sample = [
      "export function AuditViewer({ items = [] }) { return null; }",
      "export const Home = ({ counts = { running: 1 }, title = 'x', errors = {} }) => null;",
      "function helper({ rows = [] }) { return rows; }",
    ].join("\n");
    expect(literalDefaultProps(sample, "sample.tsx")).toEqual(["AuditViewer.items", "Home.counts"]);
    const offenders = Object.entries(pageSources)
      .flatMap(([path, source]) => literalDefaultProps(source, path).map((hit) => `${rel(path)}:${hit}`))
      .filter((hit) => !(hit in DEFAULT_PROP_ALLOWLIST));
    expect(offenders, "pass real data from a fetch instead of a default").toEqual([]);
  }, 60_000);

  it('renders "Sample data" and "Coming soon" only in the demo', () => {
    const offenders = Object.entries(userSources)
      .filter(([, source]) => /sample data|coming soon/i.test(source))
      .map(([path]) => rel(path));
    expect(offenders).toEqual([]);
  });

  it("allowlists only files that exist and still need it", () => {
    for (const file of Object.keys(NO_FETCH_ALLOWLIST)) {
      const source = pageSources[`../${file}`];
      expect(source, file).toBeDefined();
      expect(SHOWS_STATE.test(source ?? "") && !FETCHES.test(source ?? ""), `${file} no longer needs its allowlist entry`).toBe(true);
    }
    for (const file of Object.keys(STATIC_DEMO_IMPORT_ALLOWLIST)) {
      const source = userSources[file.startsWith("shell/") ? `./${file.slice("shell/".length)}` : `../${file}`];
      expect(source, file).toBeDefined();
      expect(/from ["'](\.\.\/)+demo\//.test(source ?? ""), `${file} no longer needs its allowlist entry`).toBe(true);
    }
  });
});

describe("honest screens: planned routes", () => {
  const today = new Date().toISOString().slice(0, 10);
  const routes = loadModules().flatMap((m) => m.routes.map((r) => ({ module: m.id, path: r.path, element: r.element })));
  const plannedPaths = new Set(plannedRoutes.map((r) => r.path));

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

  it("links no planned route from a workspace", () => {
    const config = loadWorkspaceConfig();
    const linked = config.workspaces.flatMap((w) => w.entries.filter((to) => plannedPaths.has(to)).map((to) => `${w.id} ${to}`));
    expect(linked).toEqual([]);
  });

  it("gives no planned route a card on the admin home", () => {
    const planned = adminCards.filter((card) => plannedPaths.has(card.to));
    expect(planned.length, "the home filters these out").toBeGreaterThan(0);
    // AdminHome renders adminCards without the planned ones; modules/admin/module.test.tsx checks the rendered links.
  });
});

describe("honest screens: build", () => {
  // Rows the job monitor once showed as defaults in every build. They now live in src/demo/handlers/workflow.ts.
  const markers = ["rows-1m", "400000 / 1000000", "workflow.workitem.escalated"];

  it("keeps the workflow sample rows out of every source the production build compiles", () => {
    const offenders = Object.entries(userSources).flatMap(([path, source]) => markers.filter((m) => source.includes(m)).map((m) => `${rel(path)}: ${m}`));
    expect(offenders).toEqual([]);
  });

  it("loads src/demo/ only in the demo build", () => {
    const main = userSources["../main.tsx"] ?? "";
    expect(main).toContain('const demo = import.meta.env.MODE === "demo";');
    const demoImports = [...main.matchAll(/import\(["']\.\/demo\/[^"']+["']\)|from ["']\.\/demo\//g)].map((m) => m[0]);
    expect(demoImports.length).toBeGreaterThan(0);
    expect(demoImports.every((i) => i.startsWith("import("))).toBe(true);
    for (const line of main.split("\n").filter((l) => l.includes('import("./demo/'))) expect(line).toMatch(/demo \?/);
    const staticDemo = Object.entries(userSources)
      .filter(([path, source]) => !path.startsWith("../demo/") && /from ["'](\.\.\/)+demo\/|from ["']\.\/demo\//.test(source))
      .map(([path]) => rel(path))
      .filter((file) => !(file in STATIC_DEMO_IMPORT_ALLOWLIST));
    expect(staticDemo).toEqual([]);
  });
});
