import { useState } from "react";
import { Alert, Badge, Button, ConfirmDialog, DataTable, FormField, PageHeader, Textarea, type DataTableColumn } from "../../shell/ui";
import { EntityLookup } from "../../shell/lookup/EntityLookup";
import { EntityName } from "../../shell/lookup/EntityName";
import { describeProblem, listCreditSettlements, reverseCreditSettlement, type CreditSettlementBatch, type CreditSettlementReversal, type Problem } from "./api/client";
import { money, UUID } from "./format";
import { LoadFailure } from "./parts";
import styles from "./pages.module.css";
import { useLoad } from "./useLoad";
import { ProblemMessage } from "../../shell/errors";

const PAGE = 50;

/** What a reversal gives back, in words for the confirmation and the result. */
export function reversalSummary(batch: Pick<CreditSettlementBatch, "creditDocument" | "total" | "currency" | "debits">): string {
  const docs = batch.debits.map((d) => d.documentNumber).join(", ");
  return `${money(batch.total, batch.currency)} goes back onto ${batch.creditDocument} and ${docs} is open again by what the settlement took off it.`;
}

/**
 * Credit notes (and other credits) applied to invoices (FR-30.1, 30-3), with the way back (duty receivables.payment.reverse): reversing a
 * settlement reopens the invoice and the credit, and a cross-currency settlement's realized exchange difference reverses with it.
 */
export function CreditSettlementsPage() {
  const [customer, setCustomer] = useState("");
  const valid = customer.trim() === "" || UUID.test(customer.trim());
  const key = valid ? customer.trim() : "";
  const state = useLoad((signal) => listCreditSettlements({ customer: key }, signal), key);
  const [asking, setAsking] = useState<CreditSettlementBatch | null>(null);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState<string | null>(null);
  const [done, setDone] = useState<{ batch: CreditSettlementBatch; result: CreditSettlementReversal } | null>(null);
  const [failure, setFailure] = useState<Problem | null>(null);
  const tooLong = reason.length > 500;

  async function reverse(batch: CreditSettlementBatch) {
    setAsking(null);
    setBusy(batch.batchId);
    setFailure(null);
    try {
      const result = await reverseCreditSettlement(batch.batchId, reason.trim());
      setDone({ batch, result });
      setReason("");
      state.reload();
    } catch (error) {
      setFailure(describeProblem(error));
    } finally {
      setBusy(null);
    }
  }

  const columns: DataTableColumn<CreditSettlementBatch>[] = [
    { key: "settledAt", header: "Settled", mono: true, render: (row) => row.settledAt.slice(0, 10) },
    { key: "customerId", header: "Customer", render: (row) => <EntityName entity="customer" id={row.customerId} /> },
    { key: "creditDocument", header: "Credit", mono: true },
    { key: "debits", header: "Applied to", mono: true, render: (row) => row.debits.map((d) => d.documentNumber).join(", ") },
    { key: "total", header: "Amount", mono: true, align: "end", render: (row) => money(row.total, row.currency) },
    {
      key: "fx",
      header: "Realized FX",
      mono: true,
      align: "end",
      render: (row) => {
        const fx = row.debits.filter((d) => d.realizedFx !== undefined);
        return fx.length === 0 ? "None" : fx.map((d) => d.realizedFx).join(", ");
      },
    },
    { key: "state", header: "State", render: (row) => (row.reversed ? <Badge tone="neutral">Reversed</Badge> : <Badge tone="success">Applied</Badge>) },
    {
      key: "action",
      header: "Action",
      render: (row) =>
        row.reversed ? null : (
          <Button size="sm" variant="danger" loading={busy === row.batchId} disabled={tooLong} onClick={() => setAsking(row)} aria-label={`Reverse the settlement of ${row.creditDocument}`}>
            Reverse
          </Button>
        ),
    },
  ];

  return (
    <div className={styles.stack}>
      <PageHeader
        title="Credit settlements"
        description="Credit notes and other credits applied to invoices, newest first. Reversing one opens the invoice and the credit again; a cross-currency settlement's realized exchange difference reverses with it."
      />
      <div className={styles.toolbar}>
        <FormField id="cs-customer" label="Customer" optional error={valid ? undefined : "Choose a customer."}>
          {(c) => <EntityLookup entity="customer" {...c} value={customer} onChange={(e) => setCustomer(e.target.value)} invalid={!valid} />}
        </FormField>
        <FormField id="cs-reason" label="Reason for a reversal" optional error={tooLong ? "Use at most 500 characters." : undefined}>
          {(c) => <Textarea {...c} rows={2} value={reason} onChange={(e) => setReason(e.target.value)} invalid={tooLong} />}
        </FormField>
      </div>
      {done ? (
        <Alert tone="success" title="Credit settlement reversed" live="status">
          {money(done.result.total, done.batch.currency)} is back on {done.batch.creditDocument} ({done.result.creditStatus.toLowerCase().replace("_", " ")});{" "}
          {done.result.reopened.map((r) => `${r.documentNumber} open ${money(r.openAfter, done.batch.currency)}`).join(", ")}.
        </Alert>
      ) : null}
      {failure ? (
        <Alert tone="danger" title="Could not reverse the settlement" live="alert">
          <ProblemMessage problem={failure} />
        </Alert>
      ) : null}
      {state.error ? (
        <LoadFailure title="Could not load the credit settlements" problem={state.error} retry={state.reload} />
      ) : (
        <DataTable
          label="Credit settlements"
          columns={columns}
          rows={state.data ?? []}
          getRowKey={(row) => row.batchId}
          loading={state.loading}
          empty={{ title: "No credit settlements", description: "A credit note applied to an invoice shows up here." }}
        />
      )}
      {state.data && state.data.length >= PAGE ? <p className={styles.note}>Showing the latest {PAGE} settlements.</p> : null}
      {asking ? (
        <ConfirmDialog
          title={`Reverse the settlement of ${asking.creditDocument}?`}
          description={reversalSummary(asking)}
          confirmLabel="Reverse settlement"
          cancelLabel="Keep it"
          onConfirm={() => void reverse(asking)}
          onCancel={() => setAsking(null)}
        />
      ) : null}
    </div>
  );
}
