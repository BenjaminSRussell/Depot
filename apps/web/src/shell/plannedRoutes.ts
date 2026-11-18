// Dates come from the run's wave order. The expiry test fails only after a date has passed,
// so a slice that slips must move its date in the same PR that re-plans it.
import { matchPath } from "react-router-dom";
import { z } from "zod";
import data from "./plannedRoutes.json";

const plannedRouteSchema = z.strictObject({
  path: z.string().startsWith("/"),
  issue: z.string().min(1),
  slice: z.number().int().positive(),
  date: z.iso.date(),
  reason: z.string().min(1),
});

export type PlannedRoute = z.infer<typeof plannedRouteSchema>;

export const plannedRoutes: readonly PlannedRoute[] = z
  .array(plannedRouteSchema)
  .refine(
    (rows) => new Set(rows.map((row) => row.path)).size === rows.length,
    "A planned path is listed twice",
  )
  .parse(data);

export function plannedRouteFor(pathname: string): PlannedRoute | undefined {
  return plannedRoutes.find(
    (row) => matchPath({ path: row.path, end: false }, pathname) !== null,
  );
}
