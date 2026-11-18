import { createElement } from "react";
import { Link, type RouteObject } from "react-router-dom";
import { copy } from "../../shell/copy";
import plannedData from "../../shell/plannedRoutes.json";
import { Card, EmptyState, buttonClassName } from "../../shell/ui";

/** One entry of shell/plannedRoutes.json: a route that exists in code but has no working data source yet. */
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

export function isPlanned(path: string): boolean {
  return plannedRoutes.some((r) => r.path === path);
}

/**
 * The page a planned route renders: it says plainly that the page isn't available yet and what it will do, instead of
 * a false empty state or invented rows (UXS-01). The shell's NotAvailablePage covers missing duties and features.
 */
export function PlannedPage({ path }: { path: string }) {
  const entry = plannedRoutes.find((r) => r.path === path);
  return (
    <Card>
      <EmptyState
        icon="info"
        headingLevel={2}
        title="This page isn't available yet"
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
