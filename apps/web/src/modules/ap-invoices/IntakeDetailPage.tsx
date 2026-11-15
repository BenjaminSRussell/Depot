import { useQueries, useQuery, useQueryClient } from "@tanstack/react-query";
import { useMemo, useState, type FormEvent, type ReactNode } from "react";
import { Link, useParams } from "react-router-dom";
import { holdsDuty } from "../../shell/dutyGate";
import { ProblemMessage } from "../../shell/errors";
import { EntityName } from "../../shell/lookup/EntityName";
import { usePrincipalDuties } from "../../shell/principal";
import { Alert, Badge, Button, buttonClassName, Card, ConfirmDialog, DataTable, FormField, PageHeader, Select, Skeleton, Textarea, type DataTableColumn } from "../../shell/ui";
import styles from "../../shell/pageLayout.module.css";
import { fetchReceipt, fetchReceipts, type Receipt } from "../receiving/api/client";
import { fetchInvoices, type Invoice } from "./api/client";
import { ApiError, fetchIntake, fetchIntakes, intakePurposeLabel, intakeSourceLabel, intakeStatusLabel, rejectIntake, resolveIntake, type Intake, type IntakeLine } from "./api/intake";
import { intakeTone } from "./IntakePage";
import type { InvoiceNavState } from "./parts";

const POST_DUTY = "receiving.vendor-invoice.post";
const REVIEW_DUTY = "payables.vendor-invoice.review";
const RECEIPT_DUTY = "receiving.receipt.maintain";
const REASON_MAX = 1000;

function Detail({ term, children }: { term: string; children: ReactNode }) {
  return (
    <div className={styles.entry}>
      <dt className={styles.term}>{term}</dt>
      <dd className={styles.value}>{children}</dd>
    </div>
  );
}

/** One line of a posted receipt the received invoice can be matched to. */
export interface ReceiptLineChoice {
  key: string;
  receiptId: string;
  receiptNumber: string;
  lineNo: number;
  productId: string;
  qty: string;
  unitPrice: string;
}

/** The posted receipts of the invoice's purchase order, as lines to pick from. */
export function receiptLineChoices(receipts: Receipt[], purchaseOrderId: string | undefined): ReceiptLineChoice[] {
  return receipts
    .filter((r) => r.state === "POSTED" && (!purchaseOrderId || r.purchaseOrderId === purchaseOrderId))
    .flatMap((r) => r.lines.map((l) => ({ key: `${r.id}:${l.lineNo}`, receiptId: r.id, receiptNumber: r.receiptNumber, lineNo: l.lineNo, productId: l.productId, qty: l.qty, unitPrice: l.unitPrice })));
}

/** The first receipt line each invoice line is offered, in order, so the clerk starts from a suggestion and changes what is wrong. */
export function suggestMatches(lines: IntakeLine[], choices: ReceiptLineChoice[]): Record<string, string> {
  const out: Record<string, string> = {};
  lines.forEach((l, i) => {
    const choice = choices[i];
    if (choice) out[l.lineNo] = choice.key;
  });
  return out;
}

/** What differs between an invoice line and the receipt line it is matched to: more than was received, or another price. */
export function lineFindings(line: IntakeLine, choice: ReceiptLineChoice | undefined): string[] {
  if (!choice) return [];
  const findings: string[] = [];
  if (Number(line.quantity) > Number(choice.qty)) findings.push(`Invoiced ${line.quantity}, only ${choice.qty} received`);
  if (Number(line.unitPrice) !== Number(choice.unitPrice)) findings.push(`Price ${line.unitPrice}, the order says ${choice.unitPrice}`);
  return findings;
}

/** The post page, filled in from the received invoice and the receipt lines the clerk matched. */
export function postState(i: Intake, matches: Record<string, string>, choices: ReceiptLineChoice[]): InvoiceNavState {
  const byKey = new Map(choices.map((c) => [c.key, c]));
  const items = i.lines.flatMap((l) => {
    const c = byKey.get(matches[l.lineNo] ?? "");
    return c ? [{ vendorId: i.vendorId, currency: i.currency, receiptId: c.receiptId, receiptLineNo: c.lineNo, qty: l.quantity, unitPrice: l.unitPrice }] : [];
  });
  return { header: { legalEntityId: i.legalEntityId, invoiceNumber: i.invoiceNumber, invoiceDate: i.invoiceDate }, items };
}

export function IntakeDetailPage() {
  const { id = "" } = useParams();
  return <IntakeDetail key={id} id={id} />;
}

function IntakeDetail({ id }: { id: string }) {
  const duties = usePrincipalDuties();
  const canPost = holdsDuty(duties, POST_DUTY);
  const canReview = holdsDuty(duties, REVIEW_DUTY);
  const canReceipt = holdsDuty(duties, RECEIPT_DUTY);
  const queryClient = useQueryClient();
  const result = useQuery({ queryKey: ["ap-invoices", "intake", "one", id], queryFn: () => fetchIntake(id) });
  const intake = result.data;
  const waiting = intake?.status === "RECEIVED";
  const receipts = useQuery({
    queryKey: ["ap-invoices", "intake", "receipts", intake?.vendorId ?? ""],
    queryFn: () => fetchReceipts({ vendorId: intake?.vendorId, state: "POSTED", limit: 200 }),
    enabled: Boolean(intake) && waiting && canReceipt,
  });
  // The receipt list leaves the lines out, so each posted receipt of the order is read for its lines.
  const orderReceipts = (receipts.data?.items ?? []).filter((r) => r.state === "POSTED" && (!intake?.purchaseOrderId || r.purchaseOrderId === intake.purchaseOrderId));
  const lines = useQueries({ queries: orderReceipts.map((r) => ({ queryKey: ["receiving", "receipt", r.id], queryFn: () => fetchReceipt(r.id) })) });
  const loaded = lines.flatMap((q) => (q.data ? [q.data] : []));
  const linesPending = lines.some((q) => q.isPending);
  const linesFailed = lines.find((q) => q.isError)?.error;
  const choices = useMemo(() => receiptLineChoices(loaded, intake?.purchaseOrderId), [loaded.map((r) => r.id).join(","), intake?.purchaseOrderId]);
  const [picked, setPicked] = useState<Record<string, string> | null>(null);
  const matches = picked ?? (intake ? suggestMatches(intake.lines, choices) : {});
  const [message, setMessage] = useState<{ tone: "success" | "danger"; text?: string; problem?: unknown } | null>(null);

  const decided = async (updated: Intake, text: string) => {
    queryClient.setQueryData(["ap-invoices", "intake", "one", id], updated);
    await queryClient.invalidateQueries({ queryKey: ["ap-invoices", "intake"] });
    setMessage({ tone: "success", text });
  };

  const byKey = new Map(choices.map((c) => [c.key, c]));
  const lineColumns: DataTableColumn<IntakeLine>[] = [
    { key: "lineNo", header: "Line", mono: true },
    { key: "item", header: "Item", render: (l) => l.item || "Not named" },
    { key: "quantity", header: "Quantity", align: "end", render: (l) => `${l.quantity}${l.unit ? ` ${l.unit}` : ""}` },
    { key: "unitPrice", header: "Unit price", align: "end" },
    { key: "amount", header: "Amount", align: "end", render: (l) => `${l.amount} ${intake?.currency ?? ""}` },
  ];

  const missing = result.error instanceof ApiError && result.error.status === 404;
  const forbidden = result.error instanceof ApiError && result.error.status === 403;

  return (
    <div className={styles.stack}>
      <PageHeader
        title={intake ? `Received invoice ${intake.invoiceNumber}` : "Received invoice"}
        description="An invoice a vendor sent electronically. Nothing is posted until you match it to the goods received and post it as a vendor invoice."
        meta={intake ? <Badge tone={intakeTone[intake.status]}>{intakeStatusLabel[intake.status]}</Badge> : undefined}
        actions={
          <Link className={buttonClassName("ghost")} to="/ap-invoices/intake">
            Back to received invoices
          </Link>
        }
      />
      {message ? (
        <Alert tone={message.tone} title={message.tone === "danger" ? "The decision failed" : undefined} live="status">
          {message.problem ? <ProblemMessage problem={message.problem} /> : message.text}
        </Alert>
      ) : null}
      {result.isError ? (
        <Alert
          tone="danger"
          title={forbidden ? "You can't view received invoices" : missing ? "Received invoice not found" : "Could not load the received invoice"}
          live="alert"
          action={forbidden || missing ? undefined : <Button size="sm" onClick={() => void result.refetch()}>Try again</Button>}
        >
          <ProblemMessage problem={result.error} />
        </Alert>
      ) : !intake ? (
        <Card>
          <div aria-busy="true">
            <span className="visually-hidden">Loading the received invoice…</span>
            <Skeleton height="control" />
            <Skeleton height="control" width="1/2" />
          </div>
        </Card>
      ) : (
        <>
          <Card title="Details">
            <dl className={styles.details}>
              <Detail term="Vendor">
                <EntityName entity="vendor" id={intake.vendorId} fallback="Unknown vendor" />
              </Detail>
              <Detail term="Legal entity">
                <EntityName entity="legalEntity" id={intake.legalEntityId} fallback="Unknown legal entity" />
              </Detail>
              <Detail term="Vendor's invoice number">{intake.invoiceNumber}</Detail>
              <Detail term="Kind of document">{intakePurposeLabel[intake.purpose] ?? intake.purpose}</Detail>
              <Detail term="Invoice date">{intake.invoiceDate}</Detail>
              <Detail term="Purchase order">{intake.purchaseOrderNumber || "None named"}</Detail>
              <Detail term="Net">{`${intake.net} ${intake.currency}`}</Detail>
              <Detail term="Total">{`${intake.gross} ${intake.currency}`}</Detail>
              <Detail term="Arrived as">{intakeSourceLabel[intake.source]}</Detail>
              <Detail term="Received">{intake.receivedAt.slice(0, 10)}</Detail>
              {intake.decidedAt ? <Detail term="Decided">{intake.decidedAt.slice(0, 10)}</Detail> : null}
              {intake.decisionNote ? <Detail term="Reason for the rejection">{intake.decisionNote}</Detail> : null}
              {intake.vendorInvoiceId ? (
                <Detail term="Vendor invoice">
                  <Link to={`/ap-invoices/${encodeURIComponent(intake.vendorInvoiceId)}`}>Open the posted invoice</Link>
                </Detail>
              ) : null}
            </dl>
          </Card>
          <DataTable label="Invoice lines" columns={lineColumns} rows={intake.lines} getRowKey={(l) => l.lineNo} />
          {waiting ? (
            <>
              {canReceipt ? (
                <MatchCard
                  intake={intake}
                  choices={choices}
                  loading={receipts.isPending || linesPending}
                  failed={receipts.isError ? receipts.error : linesFailed}
                  matches={matches}
                  byKey={byKey}
                  onPick={(lineNo, key) => setPicked({ ...matches, [lineNo]: key })}
                  canPost={canPost}
                />
              ) : (
                <p>Matching to the goods received needs the receiving.receipt.maintain duty.</p>
              )}
              {canReview ? (
                <>
                  <ResolveCard intake={intake} onDone={(i) => void decided(i, "Recorded. The received invoice is posted and leaves the waiting list.")} onFailed={(problem) => setMessage({ tone: "danger", problem })} />
                  <RejectCard intake={intake} onDone={(i) => void decided(i, "Rejected. The reason is kept with the received invoice.")} onFailed={(problem) => setMessage({ tone: "danger", problem })} />
                </>
              ) : (
                <p>Recording or rejecting a received invoice needs the payables.vendor-invoice.review duty.</p>
              )}
            </>
          ) : null}
        </>
      )}
    </div>
  );
}

interface MatchProps {
  intake: Intake;
  choices: ReceiptLineChoice[];
  loading: boolean;
  failed: unknown;
  matches: Record<string, string>;
  byKey: Map<string, ReceiptLineChoice>;
  onPick(lineNo: string, key: string): void;
  canPost: boolean;
}

/** Each invoice line next to the receipt line it is matched to, with what differs, and the way on to posting the vendor invoice. */
function MatchCard({ intake, choices, loading, failed, matches, byKey, onPick, canPost }: MatchProps) {
  const state = postState(intake, matches, choices);
  const matched = state.items?.length ?? 0;
  return (
    <Card title="Match to the goods received">
      <p>
        Choose the receipt line each invoice line is for. The choices are the posted receipts of {intake.purchaseOrderNumber ? `purchase order ${intake.purchaseOrderNumber}` : "this vendor"}.
      </p>
      {failed ? (
        <Alert tone="danger" title="Could not load the receipts" live="alert">
          <ProblemMessage problem={failed} />
        </Alert>
      ) : loading ? (
        <span className="visually-hidden">Loading the receipts…</span>
      ) : choices.length === 0 ? (
        <Alert tone="warning" title="No posted receipt found">
          There is no posted receipt for this order yet. Post the goods receipt first, or reject the invoice.
        </Alert>
      ) : (
        <div className={styles.stack}>
          {intake.lines.map((l) => {
            const choice = byKey.get(matches[l.lineNo] ?? "");
            const findings = lineFindings(l, choice);
            return (
              <FormField
                key={l.lineNo}
                id={`match-${l.lineNo}`}
                label={`Line ${l.lineNo}: ${l.quantity}${l.unit ? ` ${l.unit}` : ""} at ${l.unitPrice}${l.item ? `, ${l.item}` : ""}`}
                hint={findings.length ? findings.join(". ") : choice ? "Quantity and price agree with the receipt." : undefined}
              >
                {(control) => (
                  <Select {...control} value={matches[l.lineNo] ?? ""} onChange={(e) => onPick(l.lineNo, e.target.value)}>
                    <option value="">Not matched</option>
                    {choices.map((c) => (
                      <option key={c.key} value={c.key}>
                        {`${c.receiptNumber}, line ${c.lineNo}, ${c.qty} received at ${c.unitPrice}`}
                      </option>
                    ))}
                  </Select>
                )}
              </FormField>
            );
          })}
          {canPost ? (
            matched > 0 ? (
              <Link className={buttonClassName("primary")} to="/ap-invoices/post" state={state}>
                {`Post as vendor invoice (${matched} ${matched === 1 ? "line" : "lines"})`}
              </Link>
            ) : (
              <p>Match at least one line to post it.</p>
            )
          ) : (
            <p>Posting a vendor invoice needs the receiving.vendor-invoice.post duty.</p>
          )}
        </div>
      )}
    </Card>
  );
}

const PAGES = 10;

/**
 * The invoices that can close a received invoice, as the service checks them: the vendor's, of its legal entity, posted or held for
 * audit and not already recorded against another received invoice. The one with the received invoice's number comes first.
 */
export async function closingInvoices(intake: Intake): Promise<Invoice[]> {
  const all = async (status: "POSTED" | "AUDIT_BLOCKED"): Promise<Invoice[]> => {
    const out: Invoice[] = [];
    let cursor: string | undefined;
    for (let page = 0; page < PAGES; page++) {
      const body = await fetchInvoices({ vendorId: intake.vendorId, status, cursor, limit: 200 });
      out.push(...body.items);
      cursor = body.nextCursor;
      if (!cursor) break;
    }
    return out;
  };
  const [posted, held, closed] = await Promise.all([all("POSTED"), all("AUDIT_BLOCKED"), fetchIntakes({ vendorId: intake.vendorId, status: "POSTED", limit: 200 })]);
  const taken = new Set(closed.items.filter((i) => i.id !== intake.id && i.vendorInvoiceId).map((i) => i.vendorInvoiceId));
  return [...posted, ...held]
    .filter((i) => i.legalEntityId === intake.legalEntityId && !taken.has(i.id))
    .sort((a, b) => Number(b.invoiceNumber === intake.invoiceNumber) - Number(a.invoiceNumber === intake.invoiceNumber) || b.invoiceDate.localeCompare(a.invoiceDate));
}

function ResolveCard({ intake, onDone, onFailed }: { intake: Intake; onDone(i: Intake): void; onFailed(problem: unknown): void }) {
  const invoices = useQuery({ queryKey: ["ap-invoices", "intake", "closing", intake.id], queryFn: () => closingInvoices(intake) });
  const [invoiceId, setInvoiceId] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(event: FormEvent) {
    event.preventDefault();
    if (invoiceId === "") {
      setError("Choose the vendor invoice you posted from this received invoice.");
      return;
    }
    setError("");
    setBusy(true);
    try {
      onDone(await resolveIntake(intake.id, invoiceId));
    } catch (failure) {
      onFailed(failure);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Record the posted invoice">
      <form onSubmit={(event) => void submit(event)} noValidate aria-label="Record the posted invoice">
        <FormField
          id="intake-invoice"
          label="Posted vendor invoice"
          hint="This vendor's invoices in the received invoice's legal entity that are posted or held for audit and close no other received invoice; the one with the same number is first."
          error={error || (invoices.isError ? "The vendor's invoices could not be loaded." : undefined)}
        >
          {(control) => (
            <Select {...control} value={invoiceId} onChange={(e) => setInvoiceId(e.target.value)} invalid={Boolean(error)}>
              <option value="">{invoices.isPending ? "Loading invoices…" : "Choose an invoice"}</option>
              {(invoices.data ?? []).map((i) => (
                <option key={i.id} value={i.id}>
                  {`${i.invoiceNumber}, ${i.total} ${i.currency}, ${i.invoiceDate}${i.status === "AUDIT_BLOCKED" ? ", held for audit" : ""}`}
                </option>
              ))}
            </Select>
          )}
        </FormField>
        <Button type="submit" loading={busy}>
          Mark as posted
        </Button>
      </form>
    </Card>
  );
}

function RejectCard({ intake, onDone, onFailed }: { intake: Intake; onDone(i: Intake): void; onFailed(problem: unknown): void }) {
  const [reason, setReason] = useState("");
  const [error, setError] = useState("");
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);

  function ask(event: FormEvent) {
    event.preventDefault();
    const text = reason.trim();
    if (text === "" || [...text].length > REASON_MAX) {
      setError(`Enter a reason of at most ${REASON_MAX} characters.`);
      return;
    }
    setError("");
    setConfirming(true);
  }

  async function reject() {
    setConfirming(false);
    setBusy(true);
    try {
      onDone(await rejectIntake(intake.id, reason.trim()));
    } catch (failure) {
      onFailed(failure);
    } finally {
      setBusy(false);
    }
  }

  return (
    <Card title="Reject it">
      <form onSubmit={ask} noValidate aria-label="Reject the received invoice">
        <FormField id="intake-reason" label="Reason" hint={`Kept with the received invoice. At most ${REASON_MAX} characters.`} error={error || undefined}>
          {(control) => <Textarea {...control} rows={3} maxLength={REASON_MAX} value={reason} onChange={(e) => setReason(e.target.value)} invalid={Boolean(error)} />}
        </FormField>
        <Button type="submit" variant="danger" loading={busy}>
          Reject received invoice
        </Button>
      </form>
      {confirming ? (
        <ConfirmDialog
          title={`Reject invoice ${intake.invoiceNumber}?`}
          description="It leaves the waiting list as rejected. The vendor is not told by this screen."
          confirmLabel="Confirm rejection"
          cancelLabel="Cancel"
          onCancel={() => setConfirming(false)}
          onConfirm={() => void reject()}
        />
      ) : null}
    </Card>
  );
}
