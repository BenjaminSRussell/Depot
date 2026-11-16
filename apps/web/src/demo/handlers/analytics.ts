// Analytics workspace dashboards (/api/v1/analytics/dashboards) and the governed metric catalogue their tiles are
// bound to. Tiles show the metric they name, so the metrics list is served too (the dashboard pages load it).
import type { Dashboard, Metric, SavedQuery } from "../../modules/analytics/api/client";
import { reply, route } from "../backend";
import { at, table, uid } from "../store";

const BASE = "/api/v1/analytics";

const metric = (metricId: string, name: string, formula: string, grain: Metric["grain"], allowedDimensions: string[], extra: Partial<Metric> = {}): Metric => ({
  metricId, version: 3, name, formula, grain, allowedDimensions, nonFinancial: false, status: "PUBLISHED", ...extra,
});

const METRICS: Metric[] = [
  metric("net_revenue", "Net revenue", "sum(credit - debit) where account_class = 'REVENUE'", "journal_line", ["legal_entity", "fiscal_period", "cost_center"], { reconciliationSource: "ledger.trial_balance" }),
  metric("gross_margin_pct", "Gross margin %", "(net_revenue - cogs) / net_revenue", "journal_line", ["legal_entity", "fiscal_period"], { reconciliationSource: "ledger.trial_balance" }),
  metric("cogs", "Cost of goods sold", "sum(debit - credit) where account_class = 'COGS'", "journal_line", ["legal_entity", "fiscal_period", "cost_center"]),
  metric("pump_units_shipped", "Pump units shipped", "sum(shipped_qty)", "sales_line", ["legal_entity", "product_family", "customer_group"], { nonFinancial: true }),
  metric("open_order_value", "Open order value", "sum(open_qty * unit_price)", "sales_line", ["legal_entity", "product_family", "customer_group"]),
  metric("on_time_delivery_pct", "On-time delivery %", "count(shipped_on_time) / count(shipped)", "sales_line", ["legal_entity", "customer_group"], { nonFinancial: true }),
  metric("machine_utilisation_pct", "Machine utilisation %", "sum(run_minutes) / sum(available_minutes)", "resource_shift", ["plant", "work_center", "shift"], { nonFinancial: true }),
  metric("scrap_rate_pct", "Scrap rate %", "sum(scrap_qty) / sum(produced_qty)", "resource_shift", ["plant", "work_center"], { nonFinancial: true }),
  metric("opex_run_rate", "Operating expense run rate", "sum(debit - credit) where account_class = 'OPEX'", "journal_line", ["legal_entity", "fiscal_period", "cost_center"], { status: "DRAFT", version: 1 }),
];

const SEED: Dashboard[] = [
  { dashboardId: "executive_overview", title: "Executive overview", refreshSeconds: 60, version: 5, updatedAt: at(-3), tiles: [
    { title: "Net revenue, year to date", metric: "net_revenue", period: "YTD" },
    { title: "Gross margin, quarter to date", metric: "gross_margin_pct", period: "QTD" },
    { title: "Pump units shipped this month", metric: "pump_units_shipped", period: "MTD" },
    { title: "Open order book", metric: "open_order_value", period: "MTD" },
  ] },
  { dashboardId: "plant_performance", title: "Plant performance", refreshSeconds: 30, version: 3, updatedAt: at(-9), tiles: [
    { title: "Machine utilisation, month to date", metric: "machine_utilisation_pct", period: "MTD" },
    { title: "Scrap rate, month to date", metric: "scrap_rate_pct", period: "MTD" },
    { title: "Units shipped, trailing 12 months", metric: "pump_units_shipped", period: "TRAILING_12M" },
  ] },
  { dashboardId: "sales_service_levels", title: "Sales and service levels", refreshSeconds: 120, version: 2, updatedAt: at(-21), tiles: [
    { title: "On-time delivery, quarter to date", metric: "on_time_delivery_pct", period: "QTD" },
    { title: "Net revenue, trailing 12 months", metric: "net_revenue", period: "TRAILING_12M" },
  ] },
  { dashboardId: "cost_watch", title: "Cost watch", refreshSeconds: 300, version: 1, updatedAt: at(-40), tiles: [
    { title: "Cost of goods sold, month to date", metric: "cogs", period: "MTD" },
    { title: "Cost of goods sold, year to date", metric: "cogs", period: "YTD" },
  ] },
];
const dashboards = new Map<string, Dashboard>(SEED.map((d) => [d.dashboardId, d]));

route("GET", `${BASE}/metrics`, () => ({ items: METRICS }));
route("GET", `${BASE}/dashboards`, () => ({ items: [...dashboards.values()] }));
route("GET", `${BASE}/dashboards/{id}`, ({ params }) => dashboards.get(params.id) ?? reply.problem(404, "ANA-0112", "No dashboard with that id"));
route("PUT", `${BASE}/dashboards/{id}`, ({ params, body }) => {
  const input = body as Pick<Dashboard, "title" | "tiles" | "refreshSeconds">;
  const current = dashboards.get(params.id);
  const next: Dashboard = { dashboardId: params.id, title: input.title, tiles: input.tiles, refreshSeconds: input.refreshSeconds, version: (current?.version ?? 0) + 1, updatedAt: at(0) };
  dashboards.set(params.id, next);
  return current ? next : reply.created(next);
});

// Current tile values (46-3), in the demo's net revenue and shipment figures. A metric without a value says why.
const TILE_VALUES: Record<string, { value: string | null; unit?: "REPORTING_CURRENCY" | "RATIO"; currency?: string; mixed?: boolean }> = {
  net_revenue: { value: "4182650.40", currency: "USD" },
  gross_margin_pct: { value: "0.3825", unit: "RATIO" },
  cogs: { value: null, mixed: true }, // the group books in USD and EUR; the tile never adds them
  pump_units_shipped: { value: "1286" },
  open_order_value: { value: "1934120.00", currency: "USD" },
  on_time_delivery_pct: { value: "0.947", unit: "RATIO" },
  machine_utilisation_pct: { value: "0.812", unit: "RATIO" },
  scrap_rate_pct: { value: "0.0213", unit: "RATIO" },
};
route("GET", `${BASE}/dashboards/{id}/values`, ({ params }) => {
  const d = dashboards.get(params.id);
  if (!d) return reply.problem(404, "ANA-0112", "No dashboard with that id");
  const to = at(0).slice(0, 10);
  return {
    dashboardId: d.dashboardId,
    title: d.title,
    refreshSeconds: d.refreshSeconds,
    computedAt: at(0),
    tiles: d.tiles.map((t) => {
      const v = TILE_VALUES[t.metric];
      return {
        title: t.title,
        metric: t.metric,
        metricName: METRICS.find((m) => m.metricId === t.metric)?.name ?? t.metric,
        period: t.period,
        from: `${to.slice(0, 4)}-01-01`,
        to,
        status: v?.mixed ? "MIXED_CURRENCY" : v ? "OK" : "NOT_MODELLED",
        value: v?.value ?? null,
        ...(v?.unit ? { unit: v.unit } : v ? { unit: "REPORTING_CURRENCY" } : {}),
        ...(v?.currency ? { currency: v.currency } : {}),
      };
    }),
  };
});

// Saved explorer queries: private to the demo user, who starts with two.
const K_SAVED = 910;
const savedQueries = table<SavedQuery>([
  { id: uid(K_SAVED, 1), name: "Revenue and margin by entity", measures: ["net_revenue", "gross_margin_pct"], dimensions: ["legal_entity"], lookbackDays: 90, createdAt: at(-20) },
  { id: uid(K_SAVED, 2), name: "Pump units by product family", measures: ["pump_units_shipped"], dimensions: ["product_family"], lookbackDays: 30, createdAt: at(-8) },
]);

route("GET", `${BASE}/saved-queries`, ({ query }) => {
  const q = (query.get("q") ?? "").trim().toLowerCase();
  return {
    items: savedQueries
      .all()
      .filter((s) => (q === "" || s.name.toLowerCase().includes(q)) && (!query.get("name") || s.name === query.get("name")))
      .sort((a, b) => a.name.localeCompare(b.name)),
  };
});
route("POST", `${BASE}/saved-queries`, ({ body }) => {
  const input = body as { name: string; measures: string[]; dimensions?: string[]; lookbackDays?: number };
  const name = (input.name ?? "").trim();
  if (savedQueries.all().some((s) => s.name.toLowerCase() === name.toLowerCase())) return reply.problem(409, "ANA-0115", "you already have a saved query with this name");
  const row = savedQueries.insert({ id: crypto.randomUUID(), name, measures: input.measures ?? [], dimensions: input.dimensions ?? [], lookbackDays: input.lookbackDays ?? 30, createdAt: at(0) });
  return reply.created(row);
});
route("GET", `${BASE}/saved-queries/{id}`, ({ params }) => savedQueries.get(params.id) ?? reply.problem(404, "ANA-0112", "saved query not found"));
route("DELETE", `${BASE}/saved-queries/{id}`, ({ params }) => (savedQueries.remove(params.id) ? reply.noContent() : reply.problem(404, "ANA-0112", "saved query not found")));

// Report schedules: the schedule form reads the list; the demo starts with none.
route("GET", `${BASE}/schedules`, () => ({ items: [] }));

// UX telemetry (#510): Cedar Ridge Pumps' buyers and AP clerks over the last month, and the two new sites set up as
// tenants of their own. Counts follow the chosen period's length, so a week shows about a quarter of a month.
const UX_STEPS: Record<string, [string, number][]> = {
  procure_to_pay: [
    ["requisition_created", 412],
    ["requisition_submitted", 368],
    ["purchase_order_created", 301],
    ["receipt_posted", 287],
    ["vendor_invoice_posted", 243],
    ["payment_proposal_created", 198],
  ],
  onboarding: [
    ["legal_entity_created", 18],
    ["fiscal_calendar_set", 17],
    ["site_created", 14],
    ["party_added", 13],
    ["product_created", 11],
    ["first_transaction", 8],
  ],
};
const UX_SECONDS: Record<string, number> = { procure_to_pay: 3 * 86400 + 4 * 3600 + 12 * 60, onboarding: 2 * 3600 + 40 * 60 };
const UX_VALIDATION: [string, string, number][] = [
  ["/ap-invoices/post", "vendorInvoiceNumber", 46],
  ["/purchasing/requisitions/new", "lines[0].quantity", 31],
  ["/purchasing/purchase-orders/new", "lines[0].unitPrice", 22],
  ["/receiving/receipts/new", "lines[0].lotNumber", 17],
  ["/party/new", "taxRegistrationNumber", 9],
  ["/product/items/new", "unitOfMeasure", 6],
];
const UX_SEARCHES: [string, number][] = [
  ["impeller 4in bronze", 27],
  ["mech seal kit 1.5", 19],
  ["cedar ridge freight", 12],
  ["vfd 15hp", 8],
  ["casing gasket ss", 5],
];
const UX_ABANDONED: [string, number][] = [
  ["/purchasing/requisitions/new", 38],
  ["/ap-invoices/post", 24],
  ["/purchasing/purchase-orders/new", 15],
  ["/party/new", 7],
];

/** The period's share of the 30-day sample, halved again for a single release; null when the dates are not valid. */
function uxScale(query: URLSearchParams): number | null {
  const from = Date.parse(`${query.get("from") ?? ""}T00:00:00Z`);
  const to = Date.parse(`${query.get("to") ?? ""}T00:00:00Z`);
  if (Number.isNaN(from) || Number.isNaN(to) || to < from) return null;
  const days = (to - from) / 86_400_000 + 1;
  return (days / 30) * ((query.get("release") ?? "") === "" ? 1 : 0.45);
}

const uxPeriodProblem = () => reply.problem(400, "COM-0001", "request failed validation", { to: "on or after from, within one year" });

route("GET", `${BASE}/ux-funnels`, ({ query }) => {
  const flow = query.get("flow") ?? "";
  const steps = UX_STEPS[flow];
  if (!steps) return reply.problem(400, "COM-0001", "request failed validation", { flow: "one of onboarding, procure_to_pay" });
  const scale = uxScale(query);
  if (scale === null) return uxPeriodProblem();
  let prev = 0;
  const out = steps.map(([step, n], i) => {
    const sessions = Math.round(n * scale);
    const conversionFromPrevious = i === 0 ? 1 : prev > 0 ? Math.min(sessions / prev, 1) : 0;
    prev = sessions;
    return { step, sessions, conversionFromPrevious };
  });
  const finished = out[out.length - 1]?.sessions ?? 0;
  return {
    flow,
    from: query.get("from"),
    to: query.get("to"),
    ...(query.get("release") ? { release: query.get("release") } : {}),
    steps: out,
    ...(finished > 0 ? { secondsToLastStep: UX_SECONDS[flow] } : {}),
  };
});

route("GET", `${BASE}/ux-signals`, ({ query }) => {
  const scale = uxScale(query);
  if (scale === null) return uxPeriodProblem();
  const n = (c: number) => Math.round(c * scale);
  // A workspace keeps its own pages (the service's routePrefix); searches are not tied to a page.
  const prefix = query.get("routePrefix") ?? "";
  if (prefix !== "" && !/^\/[a-z0-9][a-z0-9-]{0,63}$/.test(prefix)) {
    return reply.problem(400, "COM-0001", "request failed validation", { routePrefix: "one route segment such as /purchasing" });
  }
  const inWorkspace = (route: string) => prefix === "" || route === prefix || route.startsWith(`${prefix}/`);
  return {
    from: query.get("from"),
    to: query.get("to"),
    ...(query.get("release") ? { release: query.get("release") } : {}),
    ...(prefix ? { routePrefix: prefix } : {}),
    validationErrors: UX_VALIDATION.filter(([route]) => inWorkspace(route)).map(([route, field, c]) => ({ route, field, count: n(c) })).filter((v) => v.count > 0),
    noResultSearches: UX_SEARCHES.map(([q, c]) => ({ query: q, count: n(c) })).filter((v) => v.count > 0),
    abandonedForms: UX_ABANDONED.filter(([route]) => inWorkspace(route)).map(([route, c]) => ({ route, count: n(c) })).filter((v) => v.count > 0),
  };
});
