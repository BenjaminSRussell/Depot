import { describe, expect, it, vi } from "vitest";
import type { Principal } from "../src/api/principal.js";
import { aiMayUse, createDesignationClient, treatAsCui, type DataRef } from "../src/runtime/cuiDesignation.js";

const caller: Principal = { userId: "u-1", tenantId: "t-1", duties: new Set() };
const ref = (n: number): DataRef => ({ entityType: "project", recordId: `00000000-0000-4000-8000-${String(n).padStart(12, "0")}` });
const answer = (refs: DataRef[], cls: string) => ({ items: refs.map((r) => ({ ...r, class: cls, source: cls === "CUI" ? "RECORD" : "DEFAULT" })) });
const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), { status, headers: { "content-type": "application/json" } });

function client(fetchFn: typeof fetch, extra: { timeoutMs?: number; log?: () => void } = {}) {
  return createDesignationClient({ baseUrl: "http://compliance/", authHeaders: () => ({ authorization: "Bearer t" }), fetchFn, ...extra });
}

describe("the CUI designation client", () => {
  it("returns the resolver's class and source per ref, sending the caller's credentials", async () => {
    const refs = [ref(1), ref(2)];
    const fetchFn = vi.fn(async () => json({ items: [{ ...refs[0], class: "CUI", source: "TENANT" }, { ...refs[1], class: "NONE", source: "DEFAULT" }] }));
    const out = await client(fetchFn as unknown as typeof fetch).resolve(caller, refs);
    expect(out.map((o) => [o.class, o.source])).toEqual([["CUI", "TENANT"], ["NONE", "DEFAULT"]]);
    const [url, init] = fetchFn.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe("http://compliance/v1/compliance/data-designations:resolve");
    expect((init.headers as Record<string, string>)["authorization"]).toBe("Bearer t");
  });

  it.each([
    ["a network error", async () => { throw new Error("ECONNREFUSED"); }],
    ["a 503", async () => json({}, 503)],
    ["a 403", async () => json({}, 403)],
    ["a body that is not JSON", async () => new Response("<html>", { status: 200 })],
    ["fewer items than refs", async () => json({ items: [] })],
    ["an unknown class", async () => json({ items: [{ class: "P3" }, { class: "P3" }] })],
    ["no items", async () => json({})],
  ])("answers UNKNOWN for every ref on %s, and UNKNOWN is treated as CUI", async (_name, impl) => {
    const log = vi.fn();
    const out = await client(impl as unknown as typeof fetch, { log }).resolve(caller, [ref(1), ref(2)]);
    expect(out.map((o) => o.class)).toEqual(["UNKNOWN", "UNKNOWN"]);
    expect(out.every((o) => treatAsCui(o.class) && !aiMayUse(o.class))).toBe(true);
  });

  it("answers UNKNOWN when the resolver does not answer within the timeout", async () => {
    const hang = (_url: unknown, init?: RequestInit) => new Promise<Response>((_res, rej) => init?.signal?.addEventListener("abort", () => rej(new Error("aborted"))));
    const out = await client(hang as unknown as typeof fetch, { timeoutMs: 20 }).resolve(caller, [ref(1)]);
    expect(out[0]?.class).toBe("UNKNOWN");
  });

  it("answers UNKNOWN when the credentials cannot be built", async () => {
    const failing = createDesignationClient({ baseUrl: "http://c", authHeaders: () => { throw new Error("no token"); }, fetchFn: vi.fn() as unknown as typeof fetch });
    expect((await failing.resolve(caller, [ref(1)]))[0]?.class).toBe("UNKNOWN");
  });

  it("splits more than 500 refs into batches and keeps request order", async () => {
    const refs = Array.from({ length: 1001 }, (_v, i) => ref(i + 1));
    const sizes: number[] = [];
    const fetchFn = vi.fn(async (_url: unknown, init?: RequestInit) => {
      const sent = (JSON.parse(String(init?.body)) as { refs: DataRef[] }).refs;
      sizes.push(sent.length);
      return json(answer(sent, "NONE"));
    });
    const out = await client(fetchFn as unknown as typeof fetch).resolve(caller, refs);
    expect(sizes).toEqual([500, 500, 1]);
    expect(out.map((o) => o.recordId)).toEqual(refs.map((r) => r.recordId));
  });

  it("only NONE is usable by AI", () => {
    expect([treatAsCui("CUI"), treatAsCui("UNKNOWN"), treatAsCui("NONE")]).toEqual([true, true, false]);
    expect([aiMayUse("CUI"), aiMayUse("UNKNOWN"), aiMayUse("NONE")]).toEqual([false, false, true]);
  });
});
