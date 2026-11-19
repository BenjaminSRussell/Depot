import { setLookupFetcher } from "../lookup/entities";

export function currencyLookups(currencies: Record<string, string>): void {
  setLookupFetcher(async (url) => {
    const { pathname, search } = new URL(url, "http://localhost");
    const id = decodeURIComponent(pathname.split("/").pop() ?? "");
    const currency = currencies[id];
    if (!currency) return new Response(JSON.stringify({ items: [] }), { status: search === "" ? 404 : 200 });
    const body = { id, partyId: id, ID: id, name: `Record ${id}`, Name: `Record ${id}`, code: id.slice(0, 4), number: id.slice(0, 8), currency, currencyCode: currency };
    return new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });
  });
}
