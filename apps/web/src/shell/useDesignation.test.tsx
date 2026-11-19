import { renderHook, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { toMarking, useDesignation, type DesignationRef } from "./useDesignation";

type Reply = { status: number; body: unknown };

let reply: (refs: DesignationRef[]) => Reply;
let bodies: { refs: DesignationRef[] }[];

const ref = (recordId: string): DesignationRef => ({ entityType: "project", recordId });
const answer = (refs: DesignationRef[], designationClass: string, source = "RECORD") => ({
  status: 200,
  body: { items: refs.map((r) => ({ ...r, class: designationClass, source })) },
});

beforeEach(() => {
  bodies = [];
  reply = (refs) => answer(refs, "NONE", "DEFAULT");
  vi.stubGlobal(
    "fetch",
    vi.fn(async (_input: string, init: RequestInit = {}) => {
      const body = JSON.parse(init.body as string) as { refs: DesignationRef[] };
      bodies.push(body);
      const { status, body: out } = reply(body.refs);
      return new Response(JSON.stringify(out), { status, headers: { "Content-Type": "application/json" } });
    }),
  );
});

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("toMarking", () => {
  it("maps CUI, NONE and everything else, failing closed to unavailable", () => {
    expect(toMarking({ class: "CUI", source: "TENANT" })).toEqual({ kind: "cui", source: "TENANT" });
    expect(toMarking({ class: "NONE", source: "DEFAULT" })).toEqual({ kind: "none" });
    expect(toMarking({ class: "UNKNOWN" })).toEqual({ kind: "unavailable" });
    expect(toMarking({ class: "cui" })).toEqual({ kind: "unavailable" });
    expect(toMarking({ class: 7 })).toEqual({ kind: "unavailable" });
    expect(toMarking(undefined)).toEqual({ kind: "unavailable" });
    expect(toMarking(null)).toEqual({ kind: "unavailable" });
  });
});

describe("useDesignation", () => {
  it("asks once for a page of refs, without duplicates or empty ids, and maps each class", async () => {
    reply = (refs) => ({ status: 200, body: { items: [{ ...refs[0], class: "CUI", source: "RECORD" }, { ...refs[1], class: "NONE", source: "DEFAULT" }] } });
    const { result } = renderHook(() => useDesignation([ref("B"), ref("A"), ref("B"), ref("")]));
    expect(result.current.markingOf(ref("A"))).toEqual({ kind: "pending" });
    await waitFor(() => expect(result.current.markingOf(ref("A"))).toEqual({ kind: "cui", source: "RECORD" }));
    expect(result.current.markingOf(ref("B"))).toEqual({ kind: "none" });
    expect(result.current.markingOf(ref(""))).toEqual({ kind: "unavailable" });
    expect(bodies).toEqual([{ refs: [ref("A"), ref("B")] }]);
    expect(result.current.state).toBe("ready");
  });

  it("makes no request for an empty list", () => {
    renderHook(() => useDesignation([]));
    renderHook(() => useDesignation([ref("")]));
    expect(bodies).toHaveLength(0);
  });

  it("asks only for refs it has not resolved when the list grows", async () => {
    const { result, rerender } = renderHook(({ refs }) => useDesignation(refs), { initialProps: { refs: [ref("A")] } });
    await waitFor(() => expect(result.current.markingOf(ref("A"))).toEqual({ kind: "none" }));
    rerender({ refs: [ref("A"), ref("B")] });
    await waitFor(() => expect(result.current.markingOf(ref("B"))).toEqual({ kind: "none" }));
    expect(bodies).toEqual([{ refs: [ref("A")] }, { refs: [ref("B")] }]);
  });

  it("splits 501 refs into calls of 500 and 1", async () => {
    const refs = Array.from({ length: 501 }, (_, i) => ref(`P-${String(i).padStart(4, "0")}`));
    const { result } = renderHook(() => useDesignation(refs));
    await waitFor(() => expect(result.current.markingOf(refs[500]!)).toEqual({ kind: "none" }));
    expect(bodies.map((b) => b.refs.length)).toEqual([500, 1]);
  });

  it.each([
    ["403", () => ({ status: 403, body: { code: "AUTH-0003", detail: "missing duty" } })],
    ["401", () => ({ status: 401, body: {} })],
    ["500", () => ({ status: 500, body: {} })],
    ["a reply without items", () => ({ status: 200, body: {} })],
    ["a null reply", () => ({ status: 200, body: null })],
    ["items that are not a list", () => ({ status: 200, body: { items: { class: "NONE" } } })],
  ])("reads %s as unavailable, never as not CUI", async (_name, failing) => {
    reply = failing;
    const { result } = renderHook(() => useDesignation([ref("A")]));
    await waitFor(() => expect(result.current.markingOf(ref("A"))).toEqual({ kind: "unavailable" }));
    expect(result.current.state).toBe("unavailable");
    expect(bodies).toHaveLength(1);
  });

  it("reads UNKNOWN, an omitted item and a malformed class as unavailable", async () => {
    reply = (refs) => ({ status: 200, body: { items: [{ ...refs[0], class: "UNKNOWN", source: "UNRESOLVED" }, { ...refs[1], class: 5 }] } });
    const { result } = renderHook(() => useDesignation([ref("A"), ref("B"), ref("C")]));
    await waitFor(() => expect(result.current.markingOf(ref("C"))).toEqual({ kind: "unavailable" }));
    for (const id of ["A", "B", "C"]) expect(result.current.markingOf(ref(id))).toEqual({ kind: "unavailable" });
    expect(result.current.state).toBe("ready");
  });
});
