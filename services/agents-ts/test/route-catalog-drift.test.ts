import { readdirSync, readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";
import { ROUTE_CATALOG, type CatalogEntry } from "../src/domain/routeCatalog.js";

interface NavItem {
  to: string;
  duty: string | undefined;
}

interface RouteItem {
  path: string;
  duty: string | undefined;
}

interface Modules {
  count: number;
  nav: NavItem[];
  routes: RouteItem[];
}

type Entry = Pick<CatalogEntry, "routeKey" | "path" | "duty">;

const MODULES_DIR = new URL("../../../apps/web/src/modules/", import.meta.url);
const NAV_ITEM = /\{\s*label:\s*"[^"]*",\s*to:\s*"([^"]+)"(?:,\s*duty:\s*"([^"]+)")?\s*\}/g;
const ROUTE_ITEM = /\{\s*path:\s*"([^"]+)"([^\n]*)/g;
const ROUTE_DUTY = /handle:\s*\{[^}]*\bduty:\s*"([^"]+)"/;

function parseModules(): Modules {
  const out: Modules = { count: 0, nav: [], routes: [] };
  for (const dir of readdirSync(MODULES_DIR, { withFileTypes: true })) {
    if (!dir.isDirectory()) continue;
    let source: string;
    try {
      source = readFileSync(new URL(`${dir.name}/module.ts`, MODULES_DIR), "utf8");
    } catch {
      continue;
    }
    out.count += 1;
    for (const m of source.matchAll(NAV_ITEM)) out.nav.push({ to: m[1] ?? "", duty: m[2] });
    for (const m of source.matchAll(ROUTE_ITEM)) out.routes.push({ path: m[1] ?? "", duty: ROUTE_DUTY.exec(m[2] ?? "")?.[1] });
  }
  return out;
}

const underPath = (path: string, to: string): boolean => path === to || path.startsWith(`${to}/`);

function effectiveDuty(path: string, modules: Modules): string | undefined {
  const declared = modules.routes.find((r) => r.path === path && r.duty !== undefined)?.duty;
  if (declared !== undefined) return declared;
  let best: NavItem | undefined;
  for (const n of modules.nav) if (underPath(path, n.to) && (best === undefined || n.to.length > best.to.length)) best = n;
  return best?.duty;
}

function driftOf(catalog: readonly Entry[], modules: Modules): string[] {
  const drift: string[] = [];
  for (const e of catalog) {
    const backed = modules.nav.some((n) => n.to === e.path) || modules.routes.some((r) => r.path === e.path);
    if (!backed) {
      drift.push(`${e.routeKey}: path ${e.path} is neither a nav item nor a route in apps/web modules`);
      continue;
    }
    const duty = effectiveDuty(e.path, modules);
    if (duty !== e.duty) drift.push(`${e.routeKey}: catalog duty ${e.duty} differs from the apps/web duty ${duty ?? "(none)"} for ${e.path}`);
  }
  return drift;
}

describe("ROUTE_CATALOG against apps/web module.ts", () => {
  const modules = parseModules();

  it("parses the web modules, so an unmatched regex cannot pass the drift checks", () => {
    expect(modules.count).toBeGreaterThanOrEqual(50);
    expect(modules.nav.length).toBeGreaterThanOrEqual(200);
    expect(modules.routes.length).toBeGreaterThanOrEqual(200);
  });

  it.each(ROUTE_CATALOG.map((e) => [e.routeKey, e] as const))("%s keeps the path and duty the shell enforces", (_key, entry) => {
    expect(driftOf([entry], modules)).toEqual([]);
  });

  it("reports a path no module backs and a duty that differs from the nav item", () => {
    const drift = driftOf(
      [
        { routeKey: "x.gone", path: "/receivables/no-such-page", duty: "receivables.view" },
        { routeKey: "x.duty", path: "/receivables/open-items", duty: "receivables.other" },
      ],
      modules,
    );
    expect(drift).toHaveLength(2);
    expect(drift[0]).toContain("x.gone");
    expect(drift[1]).toContain("x.duty");
  });

  it("does not let a nav item without a duty satisfy a catalog entry that demands one", () => {
    const open: Modules = { count: 1, nav: [{ to: "/a", duty: undefined }], routes: [] };
    expect(driftOf([{ routeKey: "a", path: "/a", duty: "a.view" }], open)).toHaveLength(1);
  });
});
