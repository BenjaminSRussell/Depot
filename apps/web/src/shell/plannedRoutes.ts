// Dates come from the run's wave order. The expiry test fails only after a date has passed,
// so a slice that slips must move its date in the same PR that re-plans it.
import { matchPath } from "react-router-dom";
import { z } from "zod";
import data from "./plannedRoutes.json";

const plannedRouteSchema = z.strictObject({
  route: z.string().startsWith("/"),
  issue: z.number().int().positive(),
  slice: z.number().int().positive(),
  // Optional: rows without a date never expire; rows with one fail the expiry test the day after it.
  date: z.iso.date().optional(),
  reason: z.string().min(1),
});

export type PlannedRoute = z.infer<typeof plannedRouteSchema>;

export const plannedRoutes: readonly PlannedRoute[] = z
  .array(plannedRouteSchema)
  .refine(
    (rows) => new Set(rows.map((row) => row.route)).size === rows.length,
    "A planned path is listed twice",
  )
  .parse(data);

export function plannedRouteFor(pathname: string): PlannedRoute | undefined {
  return plannedRoutes.find(
    (row) => matchPath({ path: row.route, end: false }, pathname) !== null,
  );
}
