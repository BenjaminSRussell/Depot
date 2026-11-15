import { useRef, useState, type FormEvent } from "react";
import { useLocation, useNavigate } from "react-router-dom";
import { get, newSubmitKey, post } from "../api";
import { Field, money, Notice, Page, State, messageOf, useLoad } from "../ui";
import type { Order } from "./Orders";

export interface Product { productId: string; productName?: string; unit: string; unitPrice: string; currency: string; lastOrderedAt: string }
/** A product the customer's listings allow but it has not bought: no price yet, the order prices it in the unit the customer names. */
export interface ListedProduct { productId: string; productName?: string }
interface Line { choice: string; quantity: string; unit?: string }

const choiceOf = (p: Pick<Product, "productId" | "unit">): string => `${p.productId}|${p.unit}`;
const listedChoice = (p: ListedProduct): string => `${p.productId}|`;
const DEFAULT_UNIT = "EA";
const label = (p: Product): string => `${p.productName ?? "Product"} (${p.unit}, ${money(p.unitPrice, p.currency)} last time)`;

/**
 * Place an order. A product the customer has bought is reordered at the price, unit and warehouse of its last order. A product its
 * listings allow but it has not bought is offered too, in its own group: the customer names the unit and the price is the negotiated one
 * pricing gives at the time of the order. The list comes from the server and the form sends only a product, a unit and a quantity. A submit that fails and is sent again with the
 * same lines carries the same key, so it can never place two orders; changing a line is a new submit with a new key.
 */
export function NewOrder() {
  const products = useLoad(() => get<{ items: Product[]; listed?: ListedProduct[] }>("/api/v1/products"), []);
  const navigate = useNavigate();
  const again = (useLocation().state as { lines?: Array<{ productId: string; unit: string; quantity: string }> } | null)?.lines;
  const [lines, setLines] = useState<Line[] | undefined>();
  const [date, setDate] = useState("");
  const [reference, setReference] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const submitted = useRef<{ body: string; key: string }>({ body: "", key: "" });
  const today = new Date().toISOString().slice(0, 10);

  return (
    <Page title="New order">
      <State load={products} empty={(d) => d.items.length === 0 && (d.listed ?? []).length === 0}>
        {(d) => {
          const listed = d.listed ?? [];
          const firstChoice = d.items[0] ? choiceOf(d.items[0]) : listed[0] ? listedChoice(listed[0]) : "";
          const known = new Set([...d.items.map(choiceOf), ...listed.map(listedChoice)]);
          const fromAgain: Line[] = (again ?? []).filter((l) => known.has(`${l.productId}|${l.unit}`)).map((l) => ({ choice: `${l.productId}|${l.unit}`, quantity: l.quantity }));
          const current: Line[] = lines ?? fromAgain;
          const shown = current.length > 0 ? current : [{ choice: firstChoice, quantity: "1" }];
          const change = (i: number, patch: Partial<Line>) => setLines(shown.map((l, n) => (n === i ? { ...l, ...patch } : l)));
          const submit = async (e: FormEvent) => {
            e.preventDefault();
            setError("");
            if (!date) return setError("Choose the date you want the goods shipped.");
            if (shown.some((l) => !(Number(l.quantity) > 0))) return setError("Every quantity must be more than zero.");
            if (shown.some((l) => l.choice.endsWith("|") && !(l.unit ?? DEFAULT_UNIT).trim())) return setError("Enter the unit for each product you have not ordered before.");
            const body = {
              requestedShipDate: date,
              ...(reference.trim() ? { externalRef: reference.trim() } : {}),
              lines: shown.map((l) => {
                const [productId = "", bought = ""] = l.choice.split("|");
                return { productId, unit: bought || (l.unit ?? DEFAULT_UNIT).trim(), quantity: l.quantity.trim() };
              }),
            };
            const text = JSON.stringify(body);
            if (submitted.current.body !== text) submitted.current = { body: text, key: newSubmitKey() };
            setBusy(true);
            try {
              const order = await post<Order>("/api/v1/orders", body, submitted.current.key);
              navigate(`/orders/${order.id}`, { state: { placed: order.orderNumber } });
            } catch (err) {
              setError(messageOf(err));
              setBusy(false);
            }
          };
          return (
            <form onSubmit={(e) => void submit(e)} noValidate>
              <p>Products you have bought are ordered at the price of your last order. Other products listed for you are priced at your agreed prices when you place the order. The order is confirmed after a credit check.</p>
              {shown.map((l, i) => (
                <fieldset key={i}>
                  <legend>Line {i + 1}</legend>
                  <Field label={`Product ${i + 1}`}>
                    {(id) => (
                      <select id={id} value={l.choice} onChange={(e) => change(i, { choice: e.target.value })}>
                        {d.items.map((p) => <option key={choiceOf(p)} value={choiceOf(p)}>{label(p)}</option>)}
                        {listed.length > 0 ? (
                          <optgroup label="Not ordered before, priced when you order">
                            {listed.map((p) => <option key={listedChoice(p)} value={listedChoice(p)}>{p.productName ?? "Product"}</option>)}
                          </optgroup>
                        ) : null}
                      </select>
                    )}
                  </Field>
                  {l.choice.endsWith("|") ? (
                    <Field label={`Unit ${i + 1}`} hint="The unit you order this product in, such as EA.">
                      {(id) => <input id={id} maxLength={10} value={l.unit ?? DEFAULT_UNIT} onChange={(e) => change(i, { unit: e.target.value })} aria-describedby={`${id}-hint`} />}
                    </Field>
                  ) : null}
                  <Field label={`Quantity ${i + 1}`}>
                    {(id) => <input id={id} inputMode="decimal" value={l.quantity} onChange={(e) => change(i, { quantity: e.target.value })} />}
                  </Field>
                  {shown.length > 1 ? <button type="button" className="secondary" onClick={() => setLines(shown.filter((_, n) => n !== i))}>Remove line {i + 1}</button> : null}
                </fieldset>
              ))}
              <p><button type="button" className="secondary" onClick={() => setLines([...shown, { choice: firstChoice, quantity: "1" }])}>Add a line</button></p>
              <Field label="Ship on">{(id) => <input id={id} type="date" min={today} value={date} onChange={(e) => setDate(e.target.value)} />}</Field>
              <Field label="Your reference" hint="Your own purchase order number. Optional.">
                {(id) => <input id={id} maxLength={100} value={reference} onChange={(e) => setReference(e.target.value)} aria-describedby={`${id}-hint`} />}
              </Field>
              {error ? <Notice kind="error">{error}</Notice> : null}
              <button type="submit" disabled={busy}>{busy ? "Placing the order…" : "Place the order"}</button>
            </form>
          );
        }}
      </State>
    </Page>
  );
}
