import { renderHook, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { setLookupFetcher } from "../lookup/entities";
import { currencyLookups } from "./testLookup";
import { resolveCurrency, useDefaultCurrency, useDefaultLedger, type DefaultSource } from "./useDefaults";

const PO = "11111111-1111-4111-8111-111111111111";
const VENDOR = "22222222-2222-4222-8222-222222222222";
const ENTITY = "33333333-3333-4333-8333-333333333333";

const sources: DefaultSource[] = [
  { entity: "purchaseOrder", id: PO },
  { entity: "vendor", id: VENDOR },
  { entity: "legalEntity", id: ENTITY },
];

function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });
}

describe("resolveCurrency", () => {
  it("takes the first upper-case three-letter code in source order", () => {
    expect(resolveCurrency([undefined, "eur", 7, {}, "", "GBP", "JPY"])).toBe("GBP");
  });

  it("is empty when no source holds a code", () => {
    expect(resolveCurrency([undefined, "us", null])).toBe("");
  });
});

describe("useDefaultCurrency", () => {
  it("follows the purchase order before the vendor and the legal entity", async () => {
    currencyLookups({ [PO]: "EUR", [VENDOR]: "GBP", [ENTITY]: "JPY" });
    const { result } = renderHook(() => useDefaultCurrency(sources));
    await waitFor(() => expect(result.current).toBe("EUR"));
  });

  it("falls to the next source when an earlier one holds no currency", async () => {
    currencyLookups({ [ENTITY]: "JPY" });
    const { result } = renderHook(() => useDefaultCurrency(sources));
    await waitFor(() => expect(result.current).toBe("JPY"));
  });

  it("stays empty when a lookup fails", async () => {
    const fetcher = vi.fn(async () => json({}, 500));
    setLookupFetcher(fetcher);
    const { result } = renderHook(() => useDefaultCurrency(sources));
    await waitFor(() => expect(fetcher).toHaveBeenCalled());
    expect(result.current).toBe("");
  });

  it("stays empty when the read is denied", async () => {
    const fetcher = vi.fn(async () => json({}, 403));
    setLookupFetcher(fetcher);
    const { result } = renderHook(() => useDefaultCurrency(sources));
    await waitFor(() => expect(fetcher).toHaveBeenCalled());
    expect(result.current).toBe("");
  });

  it("asks nothing for an empty or non-UUID id", async () => {
    const fetcher = vi.fn(async () => json({}));
    setLookupFetcher(fetcher);
    const { result } = renderHook(() =>
      useDefaultCurrency([
        { entity: "vendor", id: "" },
        { entity: "legalEntity", id: "not-a-uuid" },
        { entity: "customer", id: undefined },
      ]),
    );
    expect(result.current).toBe("");
    expect(fetcher).not.toHaveBeenCalled();
  });

  it("drops the old default at once when the source changes", async () => {
    currencyLookups({ [VENDOR]: "GBP", [ENTITY]: "JPY" });
    const { result, rerender } = renderHook(({ id }) => useDefaultCurrency([{ entity: "legalEntity", id }]), { initialProps: { id: VENDOR } });
    await waitFor(() => expect(result.current).toBe("GBP"));
    rerender({ id: ENTITY });
    expect(result.current).toBe("");
    await waitFor(() => expect(result.current).toBe("JPY"));
  });
});

describe("useDefaultLedger", () => {
  const ledger = (id: string) => ({ id, name: `Ledger ${id}`, code: id.slice(0, 2) });

  it("returns the ledger of a legal entity that has exactly one", async () => {
    setLookupFetcher(async () => json({ items: [ledger("a1")] }));
    const { result } = renderHook(() => useDefaultLedger(ENTITY));
    await waitFor(() => expect(result.current).toBe("a1"));
  });

  it("returns nothing when the legal entity has two ledgers", async () => {
    const fetcher = vi.fn(async () => json({ items: [ledger("a1"), ledger("b2")] }));
    setLookupFetcher(fetcher);
    const { result } = renderHook(() => useDefaultLedger(ENTITY));
    await waitFor(() => expect(fetcher).toHaveBeenCalled());
    expect(result.current).toBe("");
  });

  it("returns nothing when the list fails or the entity is not chosen", async () => {
    const fetcher = vi.fn(async () => json({}, 500));
    setLookupFetcher(fetcher);
    const failing = renderHook(() => useDefaultLedger(ENTITY));
    await waitFor(() => expect(fetcher).toHaveBeenCalled());
    expect(failing.result.current).toBe("");
    fetcher.mockClear();
    const unchosen = renderHook(() => useDefaultLedger(""));
    expect(unchosen.result.current).toBe("");
    expect(fetcher).not.toHaveBeenCalled();
  });
});
