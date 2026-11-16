import { useEffect, useMemo, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router-dom";
import { get, newSubmitKey, post } from "../api";
import { display, fromMinor, minorDigits, toMinor } from "../money";
import { loadStripeJs, stripeErrorText, type StripeElements, type StripeInstance } from "../stripe";
import { day, messageOf, Notice, Page, State, useLoad } from "../ui";
import type { OpenItem } from "./Account";

/** A card or bank account payment as receivables answers it. The client secret is on the create answer only. */
export interface PortalPayment {
  id: string;
  provider: string;
  method: "CARD" | "ACH";
  status: "REQUIRES_PAYMENT" | "PROCESSING" | "SUCCEEDED" | "FAILED" | "REFUNDED" | "DISPUTED";
  amount: string;
  currency: string;
  cardBrand?: string;
  last4?: string;
  bankName?: string;
  failureMessage?: string;
  createdAt: string;
  allocations: Array<{ documentNumber: string; amount: string }>;
  clientSecret?: string;
  publishableKey?: string;
  stripeAccount?: string;
}

/** The demo build (vite --mode demo) never loads Stripe.js: a stub stands in for the payment element. */
export const DEMO = import.meta.env.MODE === "demo";

export const STATUS_TEXT: Record<PortalPayment["status"], string> = {
  REQUIRES_PAYMENT: "Waiting for confirmation",
  PROCESSING: "Processing",
  SUCCEEDED: "Paid",
  FAILED: "Failed",
  REFUNDED: "Refunded",
  DISPUTED: "Returned or disputed",
};

const BRANDS: Record<string, string> = { visa: "Visa", mastercard: "Mastercard", amex: "American Express", discover: "Discover" };

export function paidWith(p: PortalPayment): string {
  if (p.method === "ACH") return p.last4 ? `Bank account${p.bankName ? ` at ${p.bankName}` : ""} ending ${p.last4}` : "Bank account";
  const brand = p.cardBrand ? (BRANDS[p.cardBrand] ?? p.cardBrand) : "Card";
  return p.last4 ? `${brand} ending ${p.last4}` : brand;
}

/** Whether an open item is one of the invoice numbers the customer picked (an installment INV-1/2 belongs to INV-1). */
function ofInvoice(item: OpenItem, numbers: string[]): boolean {
  return numbers.some((n) => item.documentNumber === n || item.documentNumber.startsWith(`${n}/`));
}

interface Row {
  item: OpenItem;
  chosen: boolean;
  amount: string;
}

/** Checks the amounts; answers the error of each row (empty when fine) and the total in minor units. */
export function checkAmounts(rows: Row[]): { errors: Record<string, string>; total: bigint; currency: string } {
  const errors: Record<string, string> = {};
  let total = 0n;
  const chosen = rows.filter((r) => r.chosen);
  const currency = chosen[0]?.item.currency ?? "";
  for (const r of chosen) {
    const cur = r.item.currency;
    const amount = toMinor(r.amount, cur);
    const open = toMinor(r.item.openAmount, cur) ?? 0n;
    if (cur !== currency) errors[r.item.id] = "Pay invoices of one currency at a time.";
    else if (amount === null) errors[r.item.id] = `Enter an amount such as ${fromMinor(open, cur)}${minorDigits(cur) === 0 ? "" : `, with at most ${minorDigits(cur)} decimals`}.`;
    else if (amount <= 0n) errors[r.item.id] = "Enter an amount above zero.";
    else if (amount > open) errors[r.item.id] = `You can pay at most ${display(r.item.openAmount, cur)} on ${r.item.documentNumber}.`;
    else total += amount;
  }
  return { errors, total, currency };
}

/** Pay one or several open invoices: the amounts first, then card or bank account on Stripe's own payment element. */
export function Pay() {
  const [params] = useSearchParams();
  const load = useLoad(() => get<{ items: OpenItem[] }>("/api/v1/open-items?status=open&limit=200"), []);
  return (
    <Page title="Pay invoices">
      <State load={load}>
        {(d) => {
          const debits = d.items.filter((i) => i.side !== "CREDIT");
          const ids = (params.get("items") ?? "").split(",").filter(Boolean);
          const numbers = (params.get("invoices") ?? "").split(",").filter(Boolean);
          const preselected = new Set(debits.filter((i) => ids.includes(i.id) || ofInvoice(i, numbers)).map((i) => i.id));
          if (debits.length === 0) return <p>You have nothing to pay.</p>;
          return <PayForm items={debits} preselected={preselected} />;
        }}
      </State>
    </Page>
  );
}

function PayForm({ items, preselected }: { items: OpenItem[]; preselected: Set<string> }) {
  const navigate = useNavigate();
  const [rows, setRows] = useState<Row[]>(() => items.map((item) => ({ item, chosen: preselected.has(item.id), amount: item.openAmount })));
  const [method, setMethod] = useState<"CARD" | "ACH">("CARD");
  const [tried, setTried] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [intent, setIntent] = useState<PortalPayment | null>(null);
  const key = useRef(newSubmitKey());
  const check = useMemo(() => checkAmounts(rows), [rows]);
  const chosen = rows.filter((r) => r.chosen);
  const achAllowed = check.currency === "" || check.currency === "USD";
  const errorList = Object.entries(check.errors);

  function change(id: string, patch: Partial<Row>) {
    setRows((prev) => prev.map((r) => (r.item.id === id ? { ...r, ...patch } : r)));
    key.current = newSubmitKey();
    setError("");
  }

  /** Back to the amounts: the abandoned payment is cancelled so it stops holding the invoices. One under way opens its status. */
  async function back(abandoned: PortalPayment) {
    try {
      await post(`/api/v1/payment-intents/${abandoned.id}/cancel`, {}, newSubmitKey());
      setIntent(null);
      key.current = newSubmitKey();
    } catch {
      navigate(`/pay/${abandoned.id}`);
    }
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    setTried(true);
    if (chosen.length === 0 || errorList.length > 0 || (method === "ACH" && !achAllowed)) return;
    setBusy(true);
    setError("");
    try {
      const body = { method, allocations: chosen.map((r) => ({ openItemId: r.item.id, amount: fromMinor(toMinor(r.amount, r.item.currency) ?? 0n, r.item.currency) })) };
      const out = await post<PortalPayment>("/api/v1/payment-intents", body, key.current);
      if (out.status !== "REQUIRES_PAYMENT" || !out.clientSecret) navigate(`/pay/${out.id}`);
      else setIntent(out);
    } catch (err) {
      setError(messageOf(err));
    } finally {
      setBusy(false);
    }
  }

  if (intent) {
    return (
      <section aria-labelledby="details-heading">
        <h2 id="details-heading">Payment details</h2>
        <p>
          You are paying {display(intent.amount, intent.currency)} for {intent.allocations.map((a) => a.documentNumber).join(", ")}
          {intent.method === "ACH" ? " from a US bank account." : " by card."}
        </p>
        <PaymentStep intent={intent} onBack={() => void back(intent)} />
      </section>
    );
  }

  return (
    <form onSubmit={submit} noValidate aria-describedby="pay-hint">
      <p id="pay-hint">Choose the invoices to pay and how much of each. You can pay part of an invoice; you cannot pay more than is open.</p>
      {tried && (errorList.length > 0 || chosen.length === 0) ? (
        <div role="alert" className="error">
          <p>Check the payment:</p>
          <ul>
            {chosen.length === 0 ? <li>Choose at least one invoice.</li> : null}
            {errorList.map(([id, msg]) => <li key={id}><a href={`#amount-${id}`}>{msg}</a></li>)}
          </ul>
        </div>
      ) : null}
      <table>
        <caption className="sr-only">Open invoices you can pay</caption>
        <thead><tr><th scope="col">Pay</th><th scope="col">Document</th><th scope="col">Due</th><th scope="col">Open</th><th scope="col">Amount to pay</th></tr></thead>
        <tbody>
          {rows.map((r) => (
            <tr key={r.item.id}>
              <td><input type="checkbox" aria-label={`Pay ${r.item.documentNumber}`} checked={r.chosen} onChange={(e) => change(r.item.id, { chosen: e.target.checked })} /></td>
              <th scope="row">{r.item.documentNumber}</th>
              <td>{day(r.item.dueDate)}</td>
              <td>{display(r.item.openAmount, r.item.currency)}</td>
              <td>
                <input
                  id={`amount-${r.item.id}`}
                  inputMode="decimal"
                  aria-label={`Amount to pay on ${r.item.documentNumber} in ${r.item.currency}`}
                  aria-invalid={tried && r.chosen && check.errors[r.item.id] ? true : undefined}
                  disabled={!r.chosen}
                  value={r.amount}
                  onChange={(e) => change(r.item.id, { amount: e.target.value })}
                />
              </td>
            </tr>
          ))}
        </tbody>
        <tfoot><tr><th scope="row" colSpan={4}>Total</th><td>{check.currency ? display(fromMinor(check.total, check.currency), check.currency) : "Nothing chosen"}</td></tr></tfoot>
      </table>
      <fieldset>
        <legend>Pay with</legend>
        <label><input type="radio" name="method" value="CARD" checked={method === "CARD"} onChange={() => { setMethod("CARD"); key.current = newSubmitKey(); }} /> Card</label>
        <label>
          <input type="radio" name="method" value="ACH" checked={method === "ACH"} disabled={!achAllowed} onChange={() => { setMethod("ACH"); key.current = newSubmitKey(); }} /> Bank account (ACH)
        </label>
        <small>{achAllowed ? "A bank account payment takes 3 to 5 business days to arrive; the invoices show as paid once your bank confirms it." : "Bank account payments are in US dollars only."}</small>
      </fieldset>
      {error ? <Notice kind="error">{error}</Notice> : null}
      <button type="submit" disabled={busy} aria-busy={busy || undefined}>{busy ? "Preparing…" : "Continue to payment details"}</button>
    </form>
  );
}

/** The payment step: Stripe's Payment Element, or the demo stub in the demo build. */
function PaymentStep({ intent, onBack }: { intent: PortalPayment; onBack: () => void }) {
  return DEMO ? <DemoPayment intent={intent} onBack={onBack} /> : <StripePayment intent={intent} onBack={onBack} />;
}

function StripePayment({ intent, onBack }: { intent: PortalPayment; onBack: () => void }) {
  const navigate = useNavigate();
  const mountRef = useRef<HTMLDivElement>(null);
  const stripe = useRef<{ instance: StripeInstance; elements: StripeElements } | null>(null);
  const [ready, setReady] = useState(false);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let live = true;
    let element: { destroy(): void } | null = null;
    if (!intent.clientSecret || !intent.publishableKey) {
      setError("The secure payment form is not available right now. Try again later or pay by bank transfer.");
      return;
    }
    loadStripeJs().then(
      (Stripe) => {
        if (!live || !mountRef.current || !intent.clientSecret || !intent.publishableKey) return;
        const instance = Stripe(intent.publishableKey, intent.stripeAccount ? { stripeAccount: intent.stripeAccount } : undefined);
        const elements = instance.elements({ clientSecret: intent.clientSecret });
        const pe = elements.create("payment");
        pe.mount(mountRef.current);
        element = pe;
        stripe.current = { instance, elements };
        setReady(true);
      },
      () => live && setError("The secure payment form could not be loaded. Check your connection and try again."),
    );
    return () => {
      live = false;
      element?.destroy();
    };
  }, [intent]);

  async function pay() {
    if (!stripe.current) return;
    setBusy(true);
    setError("");
    const { error: refused } = await stripe.current.instance.confirmPayment({
      elements: stripe.current.elements,
      // A redirect (3-D Secure, some banks) comes back to the status page; the status there comes from receivables, never from
      // the redirect itself.
      confirmParams: { return_url: `${window.location.origin}/pay/${intent.id}` },
      redirect: "if_required",
    });
    setBusy(false);
    if (refused) setError(stripeErrorText(refused));
    else navigate(`/pay/${intent.id}`);
  }

  return (
    <div>
      <div ref={mountRef} className="payment-element" aria-label="Secure payment form from Stripe" />
      {!ready && !error ? <p role="status">Loading the secure payment form…</p> : null}
      {error ? <Notice kind="error">{error}</Notice> : null}
      <p>
        <button type="button" onClick={() => void pay()} disabled={!ready || busy} aria-busy={busy || undefined}>
          {busy ? "Paying…" : `Pay ${display(intent.amount, intent.currency)}`}
        </button>{" "}
        <button type="button" className="secondary" onClick={onBack} disabled={busy}>Change the amounts</button>
      </p>
      <small>Your card or bank details go straight to our payment processor, Stripe. We never see or store them.</small>
    </div>
  );
}

/** Demo stand-in for Stripe's element: no Stripe.js and no card. A card payment is paid at once, a bank account payment processes. */
function DemoPayment({ intent, onBack }: { intent: PortalPayment; onBack: () => void }) {
  const navigate = useNavigate();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  async function pay() {
    setBusy(true);
    try {
      await post(`/demo/payment-intents/${intent.id}/confirm`, {}, newSubmitKey());
      navigate(`/pay/${intent.id}`);
    } catch (err) {
      setError(messageOf(err));
      setBusy(false);
    }
  }
  return (
    <div>
      <div className="payment-element demo-element">
        <p><strong>Demo payment form.</strong> No card is charged and no bank is debited.</p>
        <p>{intent.method === "ACH" ? "Bank account at STRIPE TEST BANK ending 6789" : "Visa ending 4242, expires 12/2030"}</p>
      </div>
      {error ? <Notice kind="error">{error}</Notice> : null}
      <p>
        <button type="button" onClick={() => void pay()} disabled={busy} aria-busy={busy || undefined}>{busy ? "Paying…" : `Pay ${display(intent.amount, intent.currency)}`}</button>{" "}
        <button type="button" className="secondary" onClick={onBack} disabled={busy}>Change the amounts</button>
      </p>
    </div>
  );
}

const POLL_MS = 3000;
const POLLS = 20;

/** Where a payment stands. It reads receivables, which hears from Stripe directly; it never takes the browser's word for it. */
export function PaymentStatus() {
  const { id = "" } = useParams();
  const [tick, setTick] = useState(0);
  const load = useLoad(() => get<PortalPayment>(`/api/v1/payment-intents/${id}`), [id, tick]);
  const status = load.data?.status;
  useEffect(() => {
    if (status !== "REQUIRES_PAYMENT" || tick >= POLLS) return;
    const t = setTimeout(() => setTick((n) => n + 1), POLL_MS);
    return () => clearTimeout(t);
  }, [status, tick]);
  return (
    <Page title="Payment">
      <State load={load}>
        {(p) => (
          <>
            <StatusMessage p={p} waitedOut={tick >= POLLS} />
            <dl>
              <dt>Amount</dt><dd>{display(p.amount, p.currency)}</dd>
              <dt>Paid with</dt><dd>{paidWith(p)}</dd>
              <dt>Invoices</dt><dd>{p.allocations.map((a) => `${a.documentNumber} (${display(a.amount, p.currency)})`).join(", ")}</dd>
              <dt>Started</dt><dd>{day(p.createdAt)}</dd>
            </dl>
            <p><Link to="/payments">All payments</Link></p>
          </>
        )}
      </State>
    </Page>
  );
}

function StatusMessage({ p, waitedOut }: { p: PortalPayment; waitedOut: boolean }) {
  const invoices = p.allocations.map((a) => a.documentNumber).join(",");
  switch (p.status) {
    case "SUCCEEDED":
      return <Notice kind="ok">Paid. Thank you. {display(p.amount, p.currency)} has been applied to your invoices.</Notice>;
    case "PROCESSING":
      return <Notice kind="ok">Your bank payment is on its way. Bank payments take 3 to 5 business days; the invoices show as paid once your bank confirms it.</Notice>;
    case "FAILED":
      return (
        <div role="alert" className="error">
          <p>The payment did not go through. {p.failureMessage ?? "Try again or use another payment method."}</p>
          <p><Link to={`/pay?invoices=${encodeURIComponent(invoices)}`}>Try again</Link></p>
        </div>
      );
    case "REFUNDED":
      return <Notice kind="ok">This payment was refunded. The invoices it paid are open again.</Notice>;
    case "DISPUTED":
      return <Notice kind="ok">This payment was returned by your bank or disputed. The invoices it paid are open again.</Notice>;
    default:
      return waitedOut ? (
        <Notice kind="ok">We have not heard from the payment processor yet. Nothing more is needed from you; check your payments again later.</Notice>
      ) : (
        <p role="status">We are waiting for the payment processor to confirm your payment. This page updates by itself.</p>
      );
  }
}

/** The customer's card and bank account payments, for the Payments page. */
export function PortalPayments() {
  const load = useLoad(() => get<{ items: PortalPayment[] }>("/api/v1/payment-intents?limit=50"), []);
  if (load.error || !load.data || load.data.items.length === 0) return null;
  return (
    <section aria-labelledby="portal-payments-heading">
      <h2 id="portal-payments-heading">Card and bank account payments</h2>
      <table>
        <caption className="sr-only">Payments you made by card or bank account in this portal</caption>
        <thead><tr><th scope="col">Started</th><th scope="col">Invoices</th><th scope="col">Paid with</th><th scope="col">Amount</th><th scope="col">Status</th></tr></thead>
        <tbody>
          {load.data.items.map((p) => (
            <tr key={p.id}>
              <th scope="row"><Link to={`/pay/${p.id}`}>{day(p.createdAt)}</Link></th>
              <td>{p.allocations.map((a) => a.documentNumber).join(", ")}</td>
              <td>{paidWith(p)}</td>
              <td>{display(p.amount, p.currency)}</td>
              <td><span className={`badge badge-${p.status.toLowerCase().replace(/_/g, "-")}`}>{STATUS_TEXT[p.status]}</span></td>
            </tr>
          ))}
        </tbody>
      </table>
    </section>
  );
}

