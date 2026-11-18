import { createElement } from "react";
import { Link, type RouteObject } from "react-router-dom";
import type { DepotModule } from "../../modules/registry";
import { copy } from "../copy";
import plannedData from "../plannedRoutes.json";
import { Card, EmptyState, buttonClassName } from "../ui";

/** One entry of shell/plannedRoutes.json: a route that exists in code but has no working data source yet (UXS-01). */
export interface PlannedRoute {
  path: string;
  /** Shown to people under "This page isn't available yet". */
  description: string;
  /** Why the route is planned, for developers. */
  reason: string;
  issue: number;
  backlog: string;
  slice: number;
  /** YYYY-MM-DD; the honest-screens test fails the day after. */
  targetDate: string;
}

export const plannedRoutes: readonly PlannedRoute[] = plannedData.routes;

const plannedPaths = new Set(plannedRoutes.map((r) => r.path));

export function isPlanned(path: string): boolean {
  return plannedPaths.has(path);
}

/**
 * The modules without their nav entries for planned routes. The shell builds the sidebar, the workspaces, All features,
 * the home page and the command palette from this, so nothing offers a page that isn't built; the routes stay, so a
 * bookmark still opens the planned page.
 */
export function withoutPlannedNav(modules: DepotModule[]): DepotModule[] {
  return modules
    .map((m) => (m.nav.some((n) => isPlanned(n.to)) ? { ...m, nav: m.nav.filter((n) => !isPlanned(n.to)) } : m))
    .filter((m) => m.nav.length > 0);
}

/**
 * The page a planned route renders: it says plainly that the page isn't available yet and what it will do, instead of
 * a false empty state or invented rows. NotAvailablePage covers missing duties and features.
 */
export function PlannedPage({ path }: { path: string }) {
  const entry = plannedRoutes.find((r) => r.path === path);
  return (
    <Card>
      <EmptyState
        icon="info"
        headingLevel={2}
        title={copy.planned.title}
        description={entry?.description}
        action={
          <Link to="/" className={buttonClassName("secondary")}>
            {copy.buttons.goHome}
          </Link>
        }
      />
    </Card>
  );
}

/** A route object for a path listed in shell/plannedRoutes.json. */
export function plannedRoute(path: string): RouteObject {
  if (!isPlanned(path)) throw new Error(`${path} is not in shell/plannedRoutes.json`);
  return { path, element: createElement(PlannedPage, { path }) };
}
