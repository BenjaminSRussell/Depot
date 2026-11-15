import { useInfiniteQuery } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { EntityName } from "../../shell/lookup/EntityName";
import { Badge, entityText, ListPage, type BadgeTone, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { ApiError, fetchIntakes, INTAKE_SOURCES, INTAKE_STATUSES, intakeSourceLabel, intakeStatusLabel, type Intake, type IntakeStatus } from "./api/intake";

export const intakeTone: Record<IntakeStatus, BadgeTone> = { RECEIVED: "info", POSTED: "success", REJECTED: "danger" };

const columns: ListColumn<Intake>[] = [
  { key: "invoiceNumber", header: "Vendor's invoice", mono: true },
  {
    key: "vendorId",
    header: "Vendor",
    sortable: false,
    render: (i) => <EntityName entity="vendor" id={i.vendorId} fallback="Unknown vendor" />,
    csv: (i) => entityText("vendor", i.vendorId),
  },
  { key: "purchaseOrderNumber", header: "Purchase order", mono: true, render: (i) => i.purchaseOrderNumber || "None named" },
  { key: "invoiceDate", header: "Invoice date" },
  { key: "gross", header: "Amount", align: "end", render: (i) => `${i.gross} ${i.currency}`, csv: (i) => i.gross, csvWith: "currency" },
  { key: "source", header: "Arrived as", render: (i) => intakeSourceLabel[i.source], csv: (i) => intakeSourceLabel[i.source] },
  {
    key: "status",
    header: "Status",
    render: (i) => <Badge tone={intakeTone[i.status]}>{intakeStatusLabel[i.status]}</Badge>,
    sortValue: (i) => intakeStatusLabel[i.status],
    csv: (i) => intakeStatusLabel[i.status],
  },
  { key: "receivedAt", header: "Received", render: (i) => i.receivedAt.slice(0, 10), csv: (i) => i.receivedAt },
  { key: "currency", header: "Currency", defaultHidden: true },
];

const filters: ListFilterDef[] = [
  { kind: "entity", key: "vendorId", label: "Vendor", entity: "vendor" },
  { kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: INTAKE_STATUSES.map((s) => ({ value: s, label: intakeStatusLabel[s] })) },
  { kind: "select", key: "source", label: "Arrived as", anyLabel: "Any way", options: INTAKE_SOURCES.map((s) => ({ value: s, label: intakeSourceLabel[s] })) },
];

function useIntakeRows(query: ListQuery): ListData<Intake> {
  const { vendorId, status, source } = query.filters;
  const result = useInfiniteQuery({
    queryKey: ["ap-invoices", "intake", { vendorId: vendorId ?? "", status: status ?? "" }],
    queryFn: ({ pageParam }) => fetchIntakes({ vendorId: vendorId || undefined, status: (status as IntakeStatus | undefined) || undefined, cursor: pageParam }),
    initialPageParam: undefined as string | undefined,
    getNextPageParam: (last) => last.nextCursor,
  });
  const rows = (result.data?.pages.flatMap((p) => p.items) ?? []).filter((i) => !source || i.source === source);
  return {
    rows,
    loading: result.isPending,
    loadingMore: result.isFetchingNextPage,
    error: result.isError ? result.error : undefined,
    forbidden: result.error instanceof ApiError && result.error.status === 403,
    hasMore: Boolean(result.hasNextPage),
    loadMore: () => void result.fetchNextPage(),
    retry: () => void result.refetch(),
  };
}

/** Vendor invoices that arrived electronically (a cXML network, or a vendor's X12 810), for an AP clerk to match, post or reject (#35). */
export function IntakePage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="ap-invoice-intake"
      title="Invoices received electronically"
      description="Invoices vendors sent through a procurement network or as an X12 810. Match each one to its purchase order and the goods received, post it as a vendor invoice, then record it here, or reject it with a reason."
      noun="received invoices"
      singular="received invoice"
      columns={columns}
      filters={filters}
      useRows={useIntakeRows}
      search={(i) => ({ number: i.invoiceNumber, po: i.purchaseOrderNumber ?? "" })}
      getRowKey={(i) => i.id}
      onRowOpen={(i) => navigate(`/ap-invoices/intake/${encodeURIComponent(i.id)}`)}
      empty={{ title: "No received invoices", description: "Invoices that arrive from a procurement network or as an X12 810 appear here." }}
    />
  );
}
