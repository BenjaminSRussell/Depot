import { useState } from "react";
import { useNavigate } from "react-router-dom";
import { Alert, Badge, Card, entityText, ListPage, type BadgeTone, type ListColumn, type ListData, type ListFilterDef, type ListQuery } from "../../shell/ui";
import { EntityName } from "../../shell/lookup/EntityName";
import { GATEWAY_STATUSES, listGatewayPayments, ReceivablesApiError, type GatewayPayment, type GatewayStatus } from "./api/client";
import { compareDecimals, money } from "./format";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";

const PAGE = 500;

export const GATEWAY_STATUS_LABELS: Record<GatewayStatus, string> = {
  REQUIRES_PAYMENT: "Waiting for the customer",
  PROCESSING: "Processing",
  SUCCEEDED: "Paid",
  FAILED: "Failed",
  REFUNDED: "Refunded",
  DISPUTED: "Disputed or returned",
};

export const GATEWAY_STATUS_TONES: Record<GatewayStatus, BadgeTone> = {
  REQUIRES_PAYMENT: "neutral",
  PROCESSING: "info",
  SUCCEEDED: "success",
  FAILED: "danger",
  REFUNDED: "warning",
  DISPUTED: "danger",
};

const BRANDS: Record<string, string> = { visa: "Visa", mastercard: "Mastercard", amex: "American Express", discover: "Discover", jcb: "JCB", diners: "Diners Club", unionpay: "UnionPay" };

/** "Visa ending 4242, expires 12/2030" or "Bank account at STRIPE TEST BANK ending 6789": the only card or bank facts Passage keeps. */
export function methodText(g: GatewayPayment): string {
  if (g.method === "ACH") {
    const bank = g.bankName ? ` at ${g.bankName}` : "";
    return g.last4 ? `Bank account${bank} ending ${g.last4}` : `Bank account (ACH)${bank}`;
  }
  const brand = g.cardBrand ? (BRANDS[g.cardBrand] ?? g.cardBrand) : "Card";
  const expiry = g.cardExpMonth && g.cardExpYear ? `, expires ${String(g.cardExpMonth).padStart(2, "0")}/${g.cardExpYear}` : "";
  return g.last4 ? `${brand} ending ${g.last4}${expiry}` : brand;
}

const statusText = (row: GatewayPayment) => GATEWAY_STATUS_LABELS[row.status] ?? row.status;
const documents = (row: GatewayPayment) => row.allocations.map((a) => a.documentNumber).join(", ");

const columns: ListColumn<GatewayPayment>[] = [
  { key: "createdAt", header: "Started", mono: true, render: (row) => row.createdAt.slice(0, 16).replace("T", " "), csv: (row) => row.createdAt },
  {
    key: "customerId",
    header: "Customer",
    sortable: false,
    render: (row) => <EntityName entity="customer" id={row.customerId} />,
    csv: (row) => entityText("customer", row.customerId),
  },
  { key: "documents", header: "Invoices", mono: true, render: documents, sortValue: documents, csv: documents },
  { key: "method", header: "Paid with", render: methodText, sortValue: methodText, csv: methodText },
  { key: "amount", header: "Amount", mono: true, align: "end", render: (row) => money(row.amount, row.currency), csv: (row) => row.amount, csvWith: "currency" },
  { key: "currency", header: "Currency", defaultHidden: true },
  {
    key: "status",
    header: "Status",
    render: (row) => (
      <span>
        <Badge tone={GATEWAY_STATUS_TONES[row.status] ?? "neutral"}>{statusText(row)}</Badge>
        {row.needsReview ? <> <Badge tone="warning">Needs review</Badge></> : null}
      </span>
    ),
    sortValue: statusText,
    csv: statusText,
  },
  { key: "failureMessage", header: "Why", render: (row) => row.reviewReason ?? row.failureMessage ?? "", csv: (row) => row.reviewReason ?? row.failureMessage ?? "" },
];

const filters: ListFilterDef[] = [
  { kind: "select", key: "status", label: "Status", anyLabel: "All statuses", options: GATEWAY_STATUSES.map((s) => ({ value: s, label: GATEWAY_STATUS_LABELS[s] })) },
  { kind: "entity", key: "customer", label: "Customer", entity: "customer" },
];

function useGatewayRows(query: ListQuery): ListData<GatewayPayment> {
  const status = (query.filters.status ?? "") as GatewayStatus | "";
  const customer = query.filters.customer ?? "";
  const [forbidden, setForbidden] = useState(false);
  const state = useLoad(async (signal) => {
    try {
      const rows = await listGatewayPayments({ status, customer: customer || undefined, limit: PAGE }, signal);
      if (!signal.aborted) setForbidden(false);
      return rows;
    } catch (error) {
      if (!signal.aborted) setForbidden(error instanceof ReceivablesApiError && error.status === 403);
      throw error;
    }
  }, `${status}|${customer}`);
  return {
    rows: state.error ? [] : (state.data ?? []),
    loading: state.loading,
    loadingMore: false,
    error: state.error ?? undefined,
    forbidden,
    hasMore: false,
    loadMore: () => undefined,
    retry: state.reload,
  };
}

/** Card and ACH payments customers started in the customer portal (#30); a paid one opens its receivables payment. */
export function GatewayPaymentsPage() {
  const navigate = useNavigate();
  return (
    <ListPage
      id="receivables-gateway-payments"
      title="Portal card and bank payments"
      description="Payments customers made by card or bank account (ACH) in the customer portal, newest first. The status comes only from the payment provider's verified notifications. A paid one became a payment applied to the invoices the customer chose; open it to see the application."
      noun="portal payments"
      singular="portal payment"
      columns={columns}
      filters={filters}
      useRows={useGatewayRows}
      search={(row) => ({ number: documents(row) })}
      getRowKey={(row) => row.id}
      onRowOpen={(row) => (row.paymentId ? navigate(`/receivables/payments/${row.paymentId}`) : undefined)}
      empty={{ title: "No portal payments", description: "When a customer pays invoices by card or bank account in the customer portal, the payment shows here." }}
    />
  );
}

/** On the payment page: the portal card or bank payment a payment came from, with the provider events handled for it. */
export function GatewayPaymentPanel({ paymentId }: { paymentId: string }) {
  const state = useLoad((signal) => listGatewayPayments({ payment: paymentId }, signal), paymentId);
  const g = state.data?.[0];
  if (!g) return null;
  return (
    <Card title="Paid in the customer portal" headingLevel={3}>
      <dl className={styles.details}>
        <div className={styles.entry}>
          <dt className={styles.term}>Paid with</dt>
          <dd className={styles.value}>{methodText(g)}</dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Status</dt>
          <dd className={styles.value}>
            <Badge tone={GATEWAY_STATUS_TONES[g.status] ?? "neutral"}>{GATEWAY_STATUS_LABELS[g.status] ?? g.status}</Badge>
          </dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Provider</dt>
          <dd className={styles.value}>{g.provider === "STRIPE" ? "Stripe" : "Adyen"}</dd>
        </div>
        <div className={styles.entry}>
          <dt className={styles.term}>Invoices the customer chose</dt>
          <dd className={`${styles.value} ${styles.mono}`}>{g.allocations.map((a) => `${a.documentNumber} ${money(a.amount, g.currency)}`).join(", ")}</dd>
        </div>
        {compareDecimals(g.refundedAmount, "0") > 0 ? (
          <div className={styles.entry}>
            <dt className={styles.term}>Refunded or returned</dt>
            <dd className={`${styles.value} ${styles.mono}`}>{money(g.refundedAmount, g.currency)}</dd>
          </div>
        ) : null}
      </dl>
      {g.needsReview && g.reviewReason ? (
        <Alert tone="warning" title="Needs review" live="status">
          {g.reviewReason}
        </Alert>
      ) : null}
    </Card>
  );
}
