import { useCallback, useEffect, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import { buttonClassName, ListPage, StatusPill, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { listPartners, type Partner, type PartnerQuery } from "./api/partners";
import { pricingLabels } from "./PartnerFormPage";

/** "±0.5 % price, ±2 days" */
export function toleranceText(p: Partner): string {
  return `±${p.tolerance.pricePct} % price, ±${p.tolerance.dateDays} ${p.tolerance.dateDays === 1 ? "day" : "days"}`;
}

export function pricingText(p: Partner): string {
  return p.pricing === "CostPlus" ? `${pricingLabels.CostPlus} ${p.markupPct} %` : pricingLabels[p.pricing];
}

const columns: ListColumn<Partner>[] = [
  { key: "direction", header: "Seller to buyer", mono: true, render: (p) => `${p.sellerEntity} → ${p.buyerEntity}` },
  { key: "accounts", header: "Customer / vendor", mono: true, render: (p) => `${p.customerAccount} / ${p.vendorAccount}` },
  { key: "pricing", header: "Pricing", render: pricingText },
  { key: "tolerance", header: "Change tolerance", render: toleranceText },
  { key: "status", header: "Status", render: (p) => <StatusPill status={p.active ? "active" : "inactive"} /> },
  { key: "version", header: "Version", align: "end", render: (p) => String(p.version) },
];

const filters: ListFilterDef[] = [
  { kind: "text", key: "entity", label: "Entity" },
  {
    kind: "select",
    key: "active",
    label: "Status",
    anyLabel: "All pairings",
    options: [
      { value: "true", label: "Active only" },
      { value: "false", label: "Inactive only" },
    ],
  },
];

/** The pairings the server filters by entity (either side) and status, loaded page by page as the person asks for more. */
function usePartnerRows(query: ListQuery): ListData<Partner> {
  const entity = query.filters.entity?.trim() || undefined;
  const active = query.filters.active === undefined || query.filters.active === "" ? undefined : query.filters.active === "true";
  const [state, setState] = useState<{ rows: Partner[]; next?: string; loading: boolean; loadingMore: boolean; error?: unknown }>({ rows: [], loading: true, loadingMore: false });
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const ctl = new AbortController();
    setState((s) => ({ ...s, loading: true, error: undefined }));
    const request: PartnerQuery = { entity, active };
    listPartners(request, ctl.signal)
      .then((page) => setState({ rows: page.items, next: page.nextCursor, loading: false, loadingMore: false }))
      .catch((error: unknown) => {
        if (!ctl.signal.aborted) setState({ rows: [], loading: false, loadingMore: false, error });
      });
    return () => ctl.abort();
  }, [entity, active, reload]);

  const loadMore = useCallback(() => {
    if (!state.next) return;
    setState((s) => ({ ...s, loadingMore: true }));
    listPartners({ entity, active, after: state.next })
      .then((page) => setState((s) => ({ ...s, rows: [...s.rows, ...page.items], next: page.nextCursor, loadingMore: false })))
      .catch((error: unknown) => setState((s) => ({ ...s, loadingMore: false, error })));
  }, [state.next, entity, active]);

  return {
    rows: state.rows,
    loading: state.loading,
    loadingMore: state.loadingMore,
    error: state.error,
    hasMore: Boolean(state.next),
    loadMore,
    retry: () => setReload((n) => n + 1),
  };
}

/** The tenant's trading-partner pairings (FR-40.1). */
export function PartnersPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="intercompany-partners"
      title="Trading partners"
      description="Each pairing links a customer in the seller entity with a vendor in the buyer entity, with the actions, pricing and change tolerance the mirror documents follow. Every policy change is a new version."
      actions={
        <Link className={buttonClassName("primary")} to="/intercompany/partners/new">
          New trading partner
        </Link>
      }
      noun="pairings"
      singular="pairing"
      columns={columns}
      filters={filters}
      useRows={usePartnerRows}
      getRowKey={(p) => p.id}
      onRowOpen={(p) => navigate(`/intercompany/partners/${p.id}`)}
      empty={{ title: "No trading partners yet", description: "Pair a customer in one entity with a vendor in another to trade between them." }}
    />
  );
}
