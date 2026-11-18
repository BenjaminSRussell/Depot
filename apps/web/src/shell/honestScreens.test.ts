/**
 * Honest screens (UXS-01, docs/ux/patterns.md): a screen shows only what it got from a real data source.
 *
 * 1. A page that renders an empty state, a stat tile or a progress value makes a real fetch call: a fetch hook, fetch
 *    itself or a function imported (as a value, not a type) from an API client, followed through the project's own
 *    hooks and loaders. Three rules: a file that shows one fetches somewhere; a component with no props that shows one
 *    fetches itself (a sibling's fetch does not count); and a component a module route renders with no props counts as
 *    having none, whatever props it declares.
 * 2. No page component has a default prop holding rows, counts or statuses, written inline or held in a constant.
 * 3. No page without a fetch renders a hard-coded list of rows: literal rows, in a constant (also through useState or
 *    useMemo) or inline, rendered as JSX per row or passed as rows=. Links and <option> pick-lists are exempt.
 * 4. "Sample data" and "Coming soon" render nowhere outside src/demo/.
 * 5. Every route in shell/plannedRoutes.json renders the planned page and has an issue, a slice and a target date that
 *    has not passed. The shell offers none of them: not in the sidebar, a workspace, All features, the home page, the
 *    command palette or an admin home card.
 * 6. Source scan for the production build (not a build): the workflow sample rows and the demo company appear in no
 *    source outside src/demo/, and src/demo/ is reached only through imports behind the demo-mode check. The build
 *    itself is checked by apps/web/scripts/check-bundle.mjs, which CI runs after `npm run build`; both read demoMarkers.json.
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
import demoMarkers from "./demoMarkers.json";
import { DELIVERY_REF, SPEC_REF } from "./plainLanguageRules";
import { Shell } from "./Shell";
import { auditByTo, loadWorkspaceConfig, resolveNavEntries, resolveWorkspaces } from "./workspaces/model";

/** Components without props that show a state without fetching, as `modules/<path>:<Component>`, with the reason. */
const NO_FETCH_ALLOWLIST: Record<string, string> = {
  "modules/hr/PerformancePages.tsx:NoLinkedWorker": "Rendered by its parent only after the worker lookup came back empty.",
};

/**
 * Files that show a state and fetch nothing anywhere, as `modules/<path>`, with the reason. Each holds presentational
 * parts whose components all take the data as props from a page that fetches it (the per-component and route checks
 * still apply to them).
 */
const NO_FETCH_FILE_ALLOWLIST: Record<string, string> = {
  "modules/eam/parts.tsx": "Shared maintenance parts; PagedTable shows the paged list its page loads with usePaged and passes in as state.",
  "modules/integration/parts.tsx": "Views of an EDI document or map-test result, passed in by the page that fetched it (message, interchange, map tester).",
  "modules/ledger/BatchLinesEditor.tsx": "Edits the journal lines JournalBatchPage holds (a loaded batch or a new draft); the empty state is a batch with no lines yet.",
  "modules/mobile-flows/FlowSimulator.tsx": "Runs, in the browser, the flow text its page passes in; its empty state is a simulation with no steps run, not stored data.",
  "modules/productcost/shared.tsx": "Shared costing tables; SplitTable and DiagnosticsTable show the split and diagnostics a costing call returned to their page.",
};

/** Pages that render a module-level list without fetching, as `modules/<path>:<constant>`, with the reason. */
const STATIC_LIST_ALLOWLIST: Record<string, string> = {
  "modules/sales/OneTimeFields.tsx:FIELDS": "The address fields of a one-time customer, rendered as inputs; form layout, not data.",
};

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
const NOT_A_PROMPT = /[:;,.!?]|\b(none|no|nothing|yet|empty|any)\b/i;

export interface PageFacts {
  /** Every component the file declares, by name (`default` for an anonymous default export). */
  components: string[];
  /** The file makes at least one real fetch call. */
  fetches: boolean;
  /** The file renders an empty state, a stat tile, a progress value or a table with an empty message. */
  showsState: boolean;
  /** Components with no props that show such a state without fetching themselves. */
  paramlessWithoutFetch: string[];
  /** Components, with or without props, that show such a state and fetch nothing (checked for route components). */
  stateWithoutFetch: string[];
  /** `Component.prop` defaults holding an array, or a non-empty object, inline or through a module constant. */
  literalDefaults: string[];
  /** Module-level array constants the file renders as list items, table rows or `rows=`. */
  staticLists: string[];
}

const parse = (source: string, file: string) => parseAst(source, { lang: file.endsWith(".tsx") ? "tsx" : "ts" }, file) as unknown as Node;

interface ValueImport {
  /** The exported name it binds: `default`, `*` for a namespace import, or the named export. */
  imported: string;
  from: string;
}

/** The value imports of a file by local name: `import type` and `import { type X }` bring nothing that can run. */
function valueImports(program: Node): Map<string, ValueImport> {
  const out = new Map<string, ValueImport>();
  for (const decl of (program.body as Node[]) ?? []) {
    if (decl.type !== "ImportDeclaration" || decl.importKind === "type") continue;
    for (const spec of decl.specifiers as Node[]) {
      if (spec.importKind === "type") continue;
      const imported = spec.type === "ImportDefaultSpecifier" ? "default" : spec.type === "ImportNamespaceSpecifier" ? "*" : String(name(spec.imported) ?? (spec.imported as Node | undefined)?.value);
      out.set(String(name(spec.local)), { imported, from: String((decl.source as Node).value) });
    }
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
 * Decides whether an imported binding fetches: anything from an API client by its path, or (for the real sources) an
 * export the project-wide pass below found to fetch, such as a module's own `useInvoices` hook. It goes by the export,
 * not the file, so importing `DataTable` from the shell's UI barrel never counts because the barrel also re-exports a
 * component that looks names up.
 */
export type FetchingImport = (binding: ValueImport, file: string) => boolean;
const apiPathOnly: FetchingImport = ({ from }) => API_MODULE.test(from);

export function analyzePage(source: string, file: string, fetchingImport: FetchingImport = apiPathOnly): PageFacts {
  const program = parse(source, file);
  const body = (program.body as Node[]) ?? [];

  const imported = valueImports(program);
  const fetchingBindings = new Set([...imported].filter(([, binding]) => fetchingImport(binding, file)).map(([local]) => local));
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
  // An empty state that only asks for input ("Choose a warehouse") claims nothing about data, so it needs no fetch. A
  // title that goes on to say something about the data ("Select a job: none have run yet") is not a prompt.
  const prompt = (node: Node) =>
    ((node.attributes as Node[]) ?? []).some(
      (a) => name(a.name) === "title" && isNode(a.value) && typeof a.value.value === "string" && PROMPT.test(a.value.value) && !NOT_A_PROMPT.test(a.value.value),
    );
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

  // Components: capitalized function declarations and function-valued constants, the function inside `memo(...)` or
  // `forwardRef(...)` (also `React.memo`), and an anonymous default export, named `default`.
  const isFunction = (node: unknown): boolean => isNode(node) && /Function/.test(String(node.type));
  const unwrap = (node: Node): Node | undefined => {
    if (isFunction(node)) return node;
    if (node.type !== "CallExpression") return undefined;
    const callee = node.callee as Node;
    const wrapper = callee.type === "Identifier" ? name(callee) : callee.type === "MemberExpression" ? name(callee.property) : undefined;
    const [inner] = (node.arguments as Node[]) ?? [];
    return wrapper && ["memo", "forwardRef"].includes(wrapper) && isNode(inner) ? unwrap(inner) : undefined;
  };
  const components: { name: string; fn: Node }[] = [];
  walk(program, (node) => {
    if (node.type === "FunctionDeclaration" && /^[A-Z]/.test(String(name(node.id)))) components.push({ name: String(name(node.id)), fn: node });
    if (node.type === "VariableDeclarator" && /^[A-Z]/.test(String(name(node.id))) && isNode(node.init)) {
      const fn = unwrap(node.init);
      if (fn) components.push({ name: String(name(node.id)), fn });
    }
    if (node.type === "ExportDefaultDeclaration" && isNode(node.declaration) && !name((node.declaration as Node).id)) {
      const fn = unwrap(node.declaration as Node);
      if (fn) components.push({ name: "default", fn });
    }
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
  // Every component, with or without props, that shows a state and fetches nothing: a route renders such a component
  // with no props, so for a route component this is a false screen however its props are declared.
  const stateWithoutFetch = components.filter(({ fn }) => some(fn, rendersState) && !some(fn, isFetchCall)).map((c) => c.name);
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

  // A module-level array of literal rows (one or more), or an inline one, rendered as JSX per row or passed as rows=.
  // A list of links (every row has `to` or `href`) is navigation, and one rendered only as <option>s is a pick-list.
  const isLink = (row: Node) => ((row.properties as Node[]) ?? []).some((p) => ["to", "href"].includes(String(name(p.key))));
  const literalRows = (value: Node | undefined) => {
    const elements = value?.type === "ArrayExpression" ? (value.elements as Node[]) : [];
    const literal = elements.length >= 1 && elements.every((e) => isNode(e) && (e.type === "ObjectExpression" || (e.type === "Literal" && typeof e.value === "string")));
    const links = elements.every((e) => isNode(e) && e.type === "ObjectExpression" && isLink(e));
    return literal && !links;
  };
  // `const [rows] = useState(SAMPLE)` and `const rows = useMemo(() => SAMPLE, [])` hold the constant too.
  const aliases = new Map<string, string>();
  walk(program, (node) => {
    if (node.type !== "VariableDeclarator" || !isNode(node.init) || node.init.type !== "CallExpression") return;
    const hook = calledName(node.init);
    const [arg] = (node.init.arguments as Node[]) ?? [];
    if (!isNode(arg)) return;
    const source = hook === "useState" ? arg : hook === "useMemo" && isFunction(arg) && isNode(arg.body) ? (arg.body as Node) : undefined;
    if (source?.type !== "Identifier" || !constants.has(String(name(source)))) return;
    const id = node.id as Node;
    const target = id.type === "ArrayPattern" ? (id.elements as Node[])[0] : id;
    if (name(target)) aliases.set(String(name(target)), String(name(source)));
  });
  const rowsConstant = (node: Node): string | undefined => {
    if (node.type !== "Identifier") return undefined;
    const constant = aliases.get(String(name(node))) ?? String(name(node));
    return literalRows(constants.get(constant)) ? constant : undefined;
  };
  const rendersRow = (node: Node) => node.type === "JSXOpeningElement" && String(name(node.name)) !== "option";
  const staticLists = new Set<string>();
  walk(program, (node) => {
    if (node.type === "JSXAttribute" && name(node.name) === "rows" && isNode(node.value) && isNode(node.value.expression)) {
      const expression = node.value.expression as Node;
      const hit = expression.type === "ArrayExpression" ? (literalRows(expression) ? "inline rows" : undefined) : rowsConstant(expression);
      if (hit) staticLists.add(hit);
    }
    if (node.type !== "CallExpression" || (node.callee as Node).type !== "MemberExpression") return;
    const callee = node.callee as Node;
    if (name(callee.property) !== "map" && name(callee.property) !== "filter") return;
    // Follow `X.filter(...).map(...)` back to X.
    let target = callee.object as Node;
    while (target.type === "CallExpression" && (target.callee as Node).type === "MemberExpression") target = (target.callee as Node).object as Node;
    const hit = target.type === "ArrayExpression" ? (literalRows(target) ? "inline rows" : undefined) : rowsConstant(target);
    const [callback] = (node.arguments as Node[]) ?? [];
    if (hit && isNode(callback) && some(callback, rendersRow)) staticLists.add(hit);
  });

  return {
    components: components.map((c) => c.name),
    fetches: body.some((stmt) => stmt.type !== "ImportDeclaration" && some(stmt, isFetchCall)),
    showsState: some(program, isState),
    paramlessWithoutFetch,
    stateWithoutFetch,
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

/** Per file, the exported names that fetch: a function or constant whose body makes a fetch call, or a re-export of one. */
const fetchingExports = new Map<string, Set<string>>([...programs.keys()].map((key) => [key, new Set<string>()]));
const leadsToFetch: FetchingImport = ({ imported, from }, file) => {
  if (API_MODULE.test(from)) return true;
  const exports = fetchingExports.get(resolveImport(from, file) ?? "");
  return exports !== undefined && (imported === "*" ? exports.size > 0 : exports.has(imported) || exports.has("*"));
};

/** One pass over a file: returns true when it found a new fetching export. */
function markFetchingExports(key: string, program: Node): boolean {
  const exports = fetchingExports.get(key) as Set<string>;
  const before = exports.size;
  const body = (program.body as Node[]) ?? [];
  const bindings = new Set([...valueImports(program)].filter(([, binding]) => leadsToFetch(binding, key)).map(([local]) => local));
  const topLevel = new Map<string, Node>();
  for (const stmt of body) {
    const decl = (stmt.type === "ExportNamedDeclaration" || stmt.type === "ExportDefaultDeclaration") && isNode(stmt.declaration) ? (stmt.declaration as Node) : stmt;
    if (decl.type === "FunctionDeclaration" && name(decl.id)) topLevel.set(String(name(decl.id)), decl);
    if (decl.type === "VariableDeclaration") for (const d of decl.declarations as Node[]) if (name(d.id) && isNode(d.init)) topLevel.set(String(name(d.id)), d.init);
  }
  const local = new Set<string>();
  const fetches = (node: Node) =>
    some(node, (n) => {
      const called = calledName(n);
      if (called !== undefined && (FETCH_CALLS.has(called) || bindings.has(called))) return true;
      return n.type === "Identifier" && (local.has(String(name(n))) || bindings.has(String(name(n))));
    });
  for (let grew = true; grew; ) {
    grew = false;
    for (const [id, node] of topLevel) if (!local.has(id) && fetches(node)) (local.add(id), (grew = true));
  }
  for (const stmt of body) {
    if (stmt.type === "ExportNamedDeclaration") {
      const decl = stmt.declaration as Node | undefined;
      if (isNode(decl)) {
        const ids = decl.type === "VariableDeclaration" ? (decl.declarations as Node[]).map((d) => name(d.id)) : [name(decl.id)];
        for (const id of ids) if (id && local.has(id)) exports.add(id);
      }
      const source = isNode(stmt.source) ? String(stmt.source.value) : undefined;
      for (const spec of (stmt.specifiers as Node[]) ?? []) {
        const inner = String(name(spec.local) ?? (spec.local as Node | undefined)?.value);
        const outer = String(name(spec.exported) ?? (spec.exported as Node | undefined)?.value);
        if (source ? leadsToFetch({ imported: inner, from: source }, key) : local.has(inner) || bindings.has(inner)) exports.add(outer);
      }
    }
    if (stmt.type === "ExportAllDeclaration" && isNode(stmt.source)) {
      const target = fetchingExports.get(resolveImport(String(stmt.source.value), key) ?? "");
      if (API_MODULE.test(String(stmt.source.value))) exports.add("*");
      for (const id of target ?? []) exports.add(id);
    }
    if (stmt.type === "ExportDefaultDeclaration" && isNode(stmt.declaration)) {
      const decl = stmt.declaration as Node;
      const id = name(decl.id) ?? (decl.type === "Identifier" ? name(decl) : undefined);
      if ((id && local.has(id)) || (!id && fetches(decl))) exports.add("default");
    }
  }
  return exports.size > before;
}

for (let grew = true; grew; ) {
  grew = false;
  for (const [key, program] of programs) if (markFetchingExports(key, program)) grew = true;
}

type Facts = Record<string, PageFacts>;
type RouteComponent = { file: string; component: string };

/** Check 1, component rule: components without props that show a state and fetch nothing, as `file:Component`. */
const paramlessOffenders = (all: Facts, allow: Record<string, string> = {}) =>
  Object.entries(all)
    .flatMap(([file, f]) => f.paramlessWithoutFetch.map((c) => `${file}:${c}`))
    .filter((hit) => !(hit in allow));
/** Check 1, route rule: components a route renders with no props that show a state and fetch nothing. */
const routeOffenders = (routes: RouteComponent[], all: Facts) =>
  routes.filter(({ file, component }) => all[file]?.stateWithoutFetch.includes(component)).map(({ file, component }) => `${file}:${component}`);
/** Check 1, file rule: files that show a state and fetch nothing anywhere. */
const fileOffenders = (all: Facts, allow: Record<string, string> = {}) =>
  Object.entries(all)
    .filter(([file, f]) => f.showsState && !f.fetches && !(file in allow))
    .map(([file]) => file);

const facts: Facts = Object.fromEntries(Object.entries(pageSources).map(([path, source]) => [rel(path), analyzePage(source, path, leadsToFetch)]));

/**
 * The imported components a module renders as a route with no props: `createElement(X)`, `createElement(X, null)` or
 * `createElement(X, {})` in its module.ts, with X imported. Such a component has nowhere to get data but a fetch,
 * whatever props it declares. A local route wrapper (`function OrdersRoute() { return createElement(OrdersPage,
 * { onOpen }); }`) holds only what the route gives it, its params, location and navigation, so every component it
 * renders counts too, with the wrapper's name as `via`. A wrapper that fetches hands its page data, so it is not followed.
 */
export function routeComponentsWithoutProps(
  source: string,
  file: string,
  fetchingImport: FetchingImport = apiPathOnly,
): { component: string; from: string; via?: string }[] {
  const program = parse(source, file);
  const imports = new Map<string, { imported: string; from: string }>();
  for (const decl of (program.body as Node[]) ?? []) {
    if (decl.type !== "ImportDeclaration" || decl.importKind === "type") continue;
    for (const spec of decl.specifiers as Node[]) {
      const imported = spec.type === "ImportDefaultSpecifier" ? "default" : name(spec.imported);
      if (imported) imports.set(String(name(spec.local)), { imported, from: String((decl.source as Node).value) });
    }
  }
  const wrappers = new Map<string, Node>();
  for (const stmt of (program.body as Node[]) ?? []) {
    if (stmt.type === "FunctionDeclaration" && /^[A-Z]/.test(String(name(stmt.id)))) wrappers.set(String(name(stmt.id)), stmt);
    if (stmt.type === "VariableDeclaration")
      for (const d of stmt.declarations as Node[]) if (/^[A-Z]/.test(String(name(d.id))) && isNode(d.init) && /Function/.test(String(d.init.type))) wrappers.set(String(name(d.id)), d.init);
  }
  const out: { component: string; from: string; via?: string }[] = [];
  const typeOf = (node: Node) => {
    const [type, props] = (node.arguments as Node[]) ?? [];
    const noProps = props === undefined || (props.type === "Literal" && props.value === null) || (props.type === "ObjectExpression" && (props.properties as unknown[]).length === 0);
    return { id: isNode(type) && type.type === "Identifier" ? String(name(type)) : undefined, noProps };
  };
  const fetching = new Set([...valueImports(program)].filter(([, binding]) => fetchingImport(binding, file)).map(([local]) => local));
  const fetches = (fn: Node) => some(fn, (n) => FETCH_CALLS.has(String(calledName(n))) || fetching.has(String(calledName(n))));
  const followWrapper = (wrapper: string, seen: Set<string>) => {
    if (seen.has(wrapper) || fetches(wrappers.get(wrapper) as Node)) return;
    seen.add(wrapper);
    walk(wrappers.get(wrapper) as Node, (node) => {
      if (calledName(node) !== "createElement") return;
      const { id } = typeOf(node);
      const found = id === undefined ? undefined : imports.get(id);
      if (found) out.push({ component: found.imported, from: found.from, via: [...seen][0] });
      else if (id !== undefined && wrappers.has(id)) followWrapper(id, seen);
    });
  };
  walk(program, (node) => {
    if (calledName(node) !== "createElement") return;
    const { id, noProps } = typeOf(node);
    if (!noProps || id === undefined) return;
    const found = imports.get(id);
    if (found) out.push({ component: found.imported, from: found.from });
    else if (wrappers.has(id)) followWrapper(id, new Set());
  });
  return out;
}

const moduleFiles = Object.keys(userSources).filter((key) => /^\.\.\/modules\/[^/]+\/module\.ts$/.test(key));
const routeComponents = moduleFiles.flatMap((key) =>
  routeComponentsWithoutProps(userSources[key] ?? "", key, leadsToFetch).map(({ component, from, via }) => {
    const target = resolveImport(from, key);
    return { file: target ? rel(target) : `unresolved ${from}`, component, module: rel(key), ...(via ? { via } : {}) };
  }),
);

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
    // Review 2: one element (m13), an inline rows= (n4), a row per <Card> (n3), a constant through useState (n5) or useMemo.
    const one = 'const alerts = [{ message: "Batch outbox.sweep failed" }];\nexport function Alerts() { return <ul>{alerts.map((a) => <li key={a.message}>{a.message}</li>)}</ul>; }';
    const inline = 'export function Jobs() { return <DataTable rows={[{ code: "a" }, { code: "b" }]} />; }';
    const cardsPerRow = 'const steps = [{ name: "A" }, { name: "B" }];\nexport function Steps() { return <div>{steps.map((s) => <Card key={s.name}>{s.name}</Card>)}</div>; }';
    const state = 'const SAMPLE = [{ code: "a" }];\nexport function Jobs() { const [rows] = useState(SAMPLE); return <DataTable rows={rows} />; }';
    const memo = 'const SAMPLE = [{ code: "a" }];\nexport function Jobs() { const rows = useMemo(() => SAMPLE, []); return <ul>{rows.map((r) => <li key={r.code}>{r.code}</li>)}</ul>; }';
    expect([steps, codes, table, options, links, one, inline, cardsPerRow, state, memo].map((code) => page(code).staticLists)).toEqual([
      ["steps"],
      ["shipped"],
      ["runs"],
      [],
      [],
      ["alerts"],
      ["inline rows"],
      ["steps"],
      ["SAMPLE"],
      ["SAMPLE"],
    ]);
  });

  it("finds components wrapped in memo or forwardRef, and an anonymous default export", () => {
    // Review 2: n11 and n12.
    const code = [
      'export default function () { return <EmptyState title="No documents yet" />; }',
      'export const Tiles = memo(() => <StatTile label="Running jobs" value={1} />);',
      'export const Field = React.forwardRef(function Field() { return <EmptyState title="No values" />; });',
    ].join("\n");
    expect(page(code).paramlessWithoutFetch).toEqual(["default", "Tiles", "Field"]);
  });

  it("exempts only a pure input prompt", () => {
    // Review 2: n8.
    const titles = ["Choose a warehouse", "Select a job: none have run yet", "Pick a period, no data yet", "Enter a date"];
    expect(titles.map((title) => page(`export function P() { return <EmptyState title="${title}" />; }`).paramlessWithoutFetch)).toEqual([[], ["P"], ["P"], []]);
  });

  it("flags a page with props that shows a state without fetching, by its file and as a route", () => {
    // Review 2: n13 (the old AuditViewer with an optional prop), n1 and n2 declare a prop, and m14 takes `props`, so the
    // component rule passes them all. The file rule and the route rule catch each one.
    const sources: Record<string, string> = {
      "AuditViewer.tsx": 'export function AuditViewer({ items }: { items?: Entry[] }) { return <DataTable rows={items ?? []} empty={{ title: "No audit entries yet" }} />; }',
      "DocsPage.tsx": 'export function DocsPage(_props: { note?: string }) { return <EmptyState title="No documents yet" />; }',
      "WorkflowHome.tsx": 'export function WorkflowHome({ title = "Workflow" }) { return <StatTile label={title} value={3} />; }',
      "Tiles.tsx": 'export function Tiles(props) { return <StatTile label="Running jobs" value={props.running ?? 1} />; }',
      "Real.tsx": 'import { useQuery } from "@tanstack/react-query";\nexport function Real({ title }: { title?: string }) { const q = useQuery({}); return <DataTable rows={q.data ?? []} empty={{ title: title ?? "None" }} />; }',
      "Orders.tsx": 'export function OrdersPage({ onOpen }) { return <EmptyState title="No orders yet" />; }',
    };
    const all = Object.fromEntries(Object.entries(sources).map(([file, code]) => [file, page(code)]));
    const moduleSource = [
      'import { createElement } from "react";',
      'import { useNavigate } from "react-router-dom";',
      'import { AuditViewer } from "./AuditViewer";',
      'import { DocsPage } from "./DocsPage";',
      'import { WorkflowHome } from "./WorkflowHome";',
      'import { Tiles } from "./Tiles";',
      'import { Real } from "./Real";',
      'import { OrdersPage } from "./Orders";',
      "function OrdersRoute() { const navigate = useNavigate(); return createElement(OrdersPage, { onOpen: (id) => navigate(id) }); }",
      "export default { routes: [",
      '  { path: "/audit", element: createElement(AuditViewer) },',
      '  { path: "/docs", element: createElement(DocsPage, null) },',
      '  { path: "/workflow", element: createElement(WorkflowHome, {}) },',
      '  { path: "/tiles", element: createElement(Tiles, { running: 2 }) },',
      '  { path: "/real", element: createElement(Real) },',
      '  { path: "/orders", element: createElement(OrdersRoute) },',
      "] };",
    ].join("\n");
    const routes = routeComponentsWithoutProps(moduleSource, "module.ts").map(({ component, from }) => ({ file: `${from.slice(2)}.tsx`, component }));
    expect(routes.map((r) => r.component)).toEqual(["AuditViewer", "DocsPage", "WorkflowHome", "Real", "OrdersPage"]);

    expect(paramlessOffenders(all)).toEqual([]);
    expect(routeOffenders(routes, all)).toEqual(["AuditViewer.tsx:AuditViewer", "DocsPage.tsx:DocsPage", "WorkflowHome.tsx:WorkflowHome", "Orders.tsx:OrdersPage"]);
    expect(fileOffenders(all)).toEqual(["AuditViewer.tsx", "DocsPage.tsx", "WorkflowHome.tsx", "Tiles.tsx", "Orders.tsx"]);
  });

  it("finds route components by their import, through a local route wrapper, but not through one that fetches", () => {
    const moduleSource = [
      'import { createElement } from "react";',
      'import { AuditViewer, DocumentsPage } from "./pages";',
      'import Home from "./Home";',
      'import { getOrder } from "./api/client";',
      'import { OrderPage, OrdersPage } from "./orders";',
      "function OrdersRoute() { return createElement(Shell, null, createElement(OrdersPage, { onOpen })); }",
      "function OrderRoute() { const [row, setRow] = useState(null); useEffect(() => void getOrder().then(setRow), []); return createElement(OrderPage, { row }); }",
      'export default { routes: [{ path: "/a", element: createElement(AuditViewer) }, { path: "/h", element: createElement(Home, null) }, { path: "/d", element: createElement(DocumentsPage, { onOpen }) }, { path: "/o", element: createElement(OrdersRoute) }, { path: "/o/1", element: createElement(OrderRoute) }] };',
    ].join("\n");
    expect(routeComponentsWithoutProps(moduleSource, "module.ts")).toEqual([
      { component: "AuditViewer", from: "./pages" },
      { component: "default", from: "./Home" },
      { component: "OrdersPage", from: "./orders", via: "OrdersRoute" },
    ]);
  });
});

describe("honest screens: source scan", () => {
  it("covers the module pages", () => {
    expect(Object.keys(pageSources).length).toBeGreaterThan(500);
  });

  it("shows an empty state, count or progress only in a component that fetches or is handed the data", () => {
    expect(paramlessOffenders(facts, NO_FETCH_ALLOWLIST), "fetch in the component that shows the state, pass it the fetched data, or delete the page").toEqual([]);
  });

  it("finds the components the module routes render with no props, each in a scanned file", () => {
    expect(routeComponents.length).toBeGreaterThan(300);
    expect(routeComponents).toContainEqual({ file: "modules/admin/SequencesPage.tsx", component: "SequencesPage", module: "modules/admin/module.ts" });
    // A component that resolves to no scanned file, or to a file that does not declare it (a barrel), would go unchecked.
    expect(routeComponents.filter(({ file, component }) => !facts[file]?.components.includes(component))).toEqual([]);
    // The module objects agree: every route element without props is one the source scan found, or a route wrapper in
    // a module.ts that fetches for its page (not followed, see routeComponentsWithoutProps).
    const wrappers = moduleFiles.flatMap((key) => [...(userSources[key] ?? "").matchAll(/^function ([A-Z]\w*)\(/gm)].map((m) => m[1]));
    const found = new Set([...routeComponents.flatMap((r) => [r.component, ...(r.via ? [r.via] : [])]), ...wrappers]);
    const loaded = loadModules().flatMap((m) =>
      m.routes.flatMap((r) => {
        if (!isValidElement(r.element) || r.element.type === PlannedPage || Object.keys(r.element.props as object).length > 0) return [];
        const type = r.element.type as { name?: string; displayName?: string };
        return [`${m.id} ${r.path}: ${type.displayName ?? type.name ?? "?"}`];
      }),
    );
    expect(loaded.length).toBeGreaterThan(300);
    expect(loaded.filter((entry) => !found.has(entry.split(": ")[1] ?? ""))).toEqual([]);
  });

  it("renders as a route with no props only components that fetch", () => {
    expect(routeOffenders(routeComponents, facts), "a route component gets no props, so it must fetch what it shows").toEqual([]);
  });

  it("shows a state only in a file that fetches", () => {
    expect(fileOffenders(facts, NO_FETCH_FILE_ALLOWLIST), "fetch the data this file shows, or allowlist a file of presentational parts with a reason").toEqual([]);
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
    for (const file of Object.keys(NO_FETCH_FILE_ALLOWLIST)) {
      expect(facts[file], file).toBeDefined();
      expect(facts[file]?.showsState && !facts[file]?.fetches, `${file} no longer needs its allowlist entry`).toBe(true);
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
  const markers: string[] = demoMarkers.markers;

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
