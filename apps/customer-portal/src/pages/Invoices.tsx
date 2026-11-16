import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { get } from "../api";
import type { OpenItem } from "./Account";
import { Badge, day, DownloadButton, money, Page, State, useLoad } from "../ui";

interface Totals { net: string; charges: string; tax: string; gross: string }
export interface Invoice {
  id: string; type: string; status: string; number: string; currency: string; invoiceDate: string; dueDate: string;
  paymentTermDays: number; totals: Totals; originalNumber?: string;
  lines?: Array<{ lineNo: number; description: string; qty: string; unitPrice: string; discountPct: string; net: string; tax: string }>;
}

/** The invoice numbers that still have something open to pay: an open debit item numbered like the invoice (or one of its installments). */
export function payableNumbers(items: OpenItem[]): Set<string> {
  const out = new Set<string>();
  for (const i of items) if (i.side !== "CREDIT") out.add(i.documentNumber.split("/")[0] as string);
  return out;
}

export function Invoices() {
  const list = useLoad(() => get<{ items: Invoice[] }>("/api/v1/invoices?limit=100"), []);
  // Which invoices are still open comes from receivables; when it cannot be read the list still shows, without Pay.
  const open = useLoad(() => get<{ items: OpenItem[] }>("/api/v1/open-items?status=open&limit=200").catch(() => ({ items: [] as OpenItem[] })), []);
  const payable = payableNumbers(open.data?.items ?? []);
  const [chosen, setChosen] = useState<string[]>([]);
  const navigate = useNavigate();
  const toggle = (n: string, on: boolean) => setChosen((prev) => (on ? [...prev, n] : prev.filter((x) => x !== n)));
  return (
    <Page
      title="Invoices"
      action={<button type="button" disabled={chosen.length === 0} onClick={() => navigate(`/pay?invoices=${encodeURIComponent(chosen.join(","))}`)}>{chosen.length > 1 ? `Pay ${chosen.length} selected` : "Pay selected"}</button>}
    >
      <State load={list} empty={(d) => d.items.length === 0}>
        {(d) => (
          <table>
            <caption className="sr-only">Your invoices and credit notes</caption>
            <thead><tr><th scope="col">Select</th><th scope="col">Number</th><th scope="col">Type</th><th scope="col">Date</th><th scope="col">Due</th><th scope="col">Total</th><th scope="col">Status</th><th scope="col">Pay</th></tr></thead>
            <tbody>
              {d.items.map((i) => {
                const canPay = i.type !== "CREDIT_NOTE" && payable.has(i.number);
                return (
                  <tr key={i.id}>
                    <td>{canPay ? <input type="checkbox" aria-label={`Select ${i.number} to pay`} checked={chosen.includes(i.number)} onChange={(e) => toggle(i.number, e.target.checked)} /> : null}</td>
                    <th scope="row"><Link to={`/invoices/${i.id}`}>{i.number}</Link></th>
                    <td>{i.type === "CREDIT_NOTE" ? "Credit note" : "Invoice"}</td>
                    <td>{day(i.invoiceDate)}</td>
                    <td>{day(i.dueDate)}</td>
                    <td>{money(i.totals.gross, i.currency)}</td>
                    <td><Badge value={i.status} /></td>
                    <td>{canPay ? <Link to={`/pay?invoices=${encodeURIComponent(i.number)}`} aria-label={`Pay ${i.number}`}>Pay</Link> : null}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
      </State>
    </Page>
  );
}

export function InvoiceDetail() {
  const { id = "" } = useParams();
  const load = useLoad(() => get<Invoice>(`/api/v1/invoices/${id}`), [id]);
  const open = useLoad(() => get<{ items: OpenItem[] }>("/api/v1/open-items?status=open&limit=200").catch(() => ({ items: [] as OpenItem[] })), []);
  const payable = payableNumbers(open.data?.items ?? []);
  return (
    <Page title="Invoice">
      <State load={load}>
        {(i) => (
          <>
            <p><Link to="/invoices">Back to invoices</Link></p>
            <DownloadButton path={`/api/v1/invoices/${i.id}/pdf`} fileName={`${i.number}.pdf`} label={`Download ${i.type === "CREDIT_NOTE" ? "credit note" : "invoice"} PDF`} />
            {i.type !== "CREDIT_NOTE" && payable.has(i.number) ? <p><Link to={`/pay?invoices=${encodeURIComponent(i.number)}`} className="button">Pay this invoice</Link></p> : null}
            <dl>
              <dt>Number</dt><dd>{i.number}</dd>
              {i.originalNumber ? <><dt>Credits invoice</dt><dd>{i.originalNumber}</dd></> : null}
              <dt>Date</dt><dd>{day(i.invoiceDate)}</dd>
              <dt>Due</dt><dd>{day(i.dueDate)} ({i.paymentTermDays} days)</dd>
              <dt>Status</dt><dd><Badge value={i.status} /></dd>
            </dl>
            <table>
              <caption className="sr-only">Lines of invoice {i.number}</caption>
              <thead><tr><th scope="col">Line</th><th scope="col">Description</th><th scope="col">Quantity</th><th scope="col">Unit price</th><th scope="col">Net</th><th scope="col">Tax</th></tr></thead>
              <tbody>{(i.lines ?? []).map((l) => <tr key={l.lineNo}><td>{l.lineNo}</td><th scope="row">{l.description}</th><td>{l.qty}</td><td>{l.unitPrice}</td><td>{l.net}</td><td>{l.tax}</td></tr>)}</tbody>
              <tfoot>
                <tr><th scope="row" colSpan={4}>Net</th><td colSpan={2}>{money(i.totals.net, i.currency)}</td></tr>
                <tr><th scope="row" colSpan={4}>Tax</th><td colSpan={2}>{money(i.totals.tax, i.currency)}</td></tr>
                <tr><th scope="row" colSpan={4}>Total</th><td colSpan={2}>{money(i.totals.gross, i.currency)}</td></tr>
              </tfoot>
            </table>
          </>
        )}
      </State>
    </Page>
  );
}
