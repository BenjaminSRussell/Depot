import { z } from "zod";
import type { DepotModule, NavItem } from "../modules/registry";

const EXTRA_ROUTES = ["/design-system", "/no-such-page"];

export function axeRoutes(modules: readonly DepotModule[], shell: readonly NavItem[]): string[] {
  const routes = [...shell.map((item) => item.to), ...modules.flatMap((module) => module.nav.map((item) => item.to)), ...EXTRA_ROUTES];
  return [...new Set(routes)].sort();
}

const isCalendarDate = (value: string) => /^\d{4}-\d{2}-\d{2}$/.test(value) && new Date(`${value}T00:00:00Z`).toISOString().startsWith(value);

const AxeAllowlistSchema = z.array(
  z.strictObject({
    route: z.string().min(1),
    rule: z.string().min(1),
    reason: z.string().min(1),
    expires: z.string().refine(isCalendarDate, "expires must be a calendar date, YYYY-MM-DD"),
  }),
);

export type AxeAllowlistRow = z.infer<typeof AxeAllowlistSchema>[number];

export function parseAxeAllowlist(raw: unknown): AxeAllowlistRow[] {
  return AxeAllowlistSchema.parse(raw);
}

export function expiredRows(rows: readonly AxeAllowlistRow[], today: string): AxeAllowlistRow[] {
  return rows.filter((row) => row.expires < today);
}
