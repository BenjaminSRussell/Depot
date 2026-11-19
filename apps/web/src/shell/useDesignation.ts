import { useEffect, useRef, useState } from "react";
import { resolveDesignations } from "../modules/compliance/api/client";

export interface DesignationRef {
  entityType: string;
  recordId: string;
}

export type Marking = { kind: "cui"; source: string } | { kind: "none" } | { kind: "unavailable" } | { kind: "pending" };

const UNAVAILABLE: Marking = { kind: "unavailable" };

export const designationKey = (ref: DesignationRef) => `${ref.entityType}\u0000${ref.recordId}`;

export function toMarking(item: unknown): Marking {
  if (typeof item !== "object" || item === null) return UNAVAILABLE;
  const { class: designationClass, source } = item as { class?: unknown; source?: unknown };
  if (designationClass === "CUI") return { kind: "cui", source: typeof source === "string" ? source : "" };
  if (designationClass === "NONE") return { kind: "none" };
  return UNAVAILABLE;
}

function wantedKey(refs: DesignationRef[]): string {
  const unique = new Map(refs.filter((r) => r.recordId !== "").map((r) => [designationKey(r), { entityType: r.entityType, recordId: r.recordId }]));
  return JSON.stringify([...unique.entries()].sort(([a], [b]) => (a < b ? -1 : 1)).map(([, ref]) => ref));
}

export function useDesignation(refs: DesignationRef[]) {
  const known = useRef(new Map<string, Marking>());
  const [version, setVersion] = useState(0);
  const [failed, setFailed] = useState(false);
  const key = wantedKey(refs);

  useEffect(() => {
    const todo = (JSON.parse(key) as DesignationRef[]).filter((r) => !known.current.has(designationKey(r)));
    if (todo.length === 0) return;
    const controller = new AbortController();
    resolveDesignations(todo, controller.signal).then(
      (items) => {
        if (controller.signal.aborted) return;
        const byKey = new Map(items.map((item) => [designationKey(item ?? { entityType: "", recordId: "" }), item]));
        for (const ref of todo) known.current.set(designationKey(ref), toMarking(byKey.get(designationKey(ref))));
        setFailed(false);
        setVersion((v) => v + 1);
      },
      () => {
        if (controller.signal.aborted) return;
        for (const ref of todo) known.current.set(designationKey(ref), UNAVAILABLE);
        setFailed(true);
        setVersion((v) => v + 1);
      },
    );
    return () => controller.abort();
  }, [key]);

  function markingOf(ref: DesignationRef): Marking {
    if (ref.recordId === "") return UNAVAILABLE;
    return known.current.get(designationKey(ref)) ?? { kind: "pending" };
  }

  return { state: failed ? ("unavailable" as const) : ("ready" as const), byKey: known.current, markingOf, version };
}
