import { useEffect, useState } from "react";
import { UUID } from "../api/http";
import { lookupDefs, resolveOption, type LookupEntityKey } from "../lookup/entities";

const CURRENCY = /^[A-Z]{3}$/;

export interface DefaultSource {
  entity: Extract<LookupEntityKey, "purchaseOrder" | "vendor" | "customer" | "legalEntity">;
  id: string | undefined;
}

export function resolveCurrency(currencies: unknown[]): string {
  const found = currencies.find((value) => typeof value === "string" && CURRENCY.test(value));
  return typeof found === "string" ? found : "";
}

interface Derived {
  key: string;
  value: string;
}

export function useDefaultCurrency(sources: DefaultSource[]): string {
  const usable = sources.filter((source) => source.id !== undefined && UUID.test(source.id));
  const key = usable.map((source) => `${source.entity}:${source.id}`).join("|");
  const [derived, setDerived] = useState<Derived>({ key: "", value: "" });
  useEffect(() => {
    if (key === "") return;
    let live = true;
    void Promise.all(usable.map((source) => resolveOption(source.entity, source.id as string))).then((options) => {
      if (live) setDerived({ key, value: resolveCurrency(options.map((option) => option?.currency)) });
    });
    return () => {
      live = false;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps -- key encodes every usable source
  }, [key]);
  return derived.key === key ? derived.value : "";
}

export function useDefaultLedger(legalEntityId: string | undefined): string {
  const entity = legalEntityId !== undefined && UUID.test(legalEntityId) ? legalEntityId : "";
  const [derived, setDerived] = useState<Derived>({ key: "", value: "" });
  useEffect(() => {
    if (entity === "") return;
    const controller = new AbortController();
    lookupDefs.ledger.search("", { legalEntityId: entity }, controller.signal).then(
      (ledgers) => {
        if (!controller.signal.aborted) setDerived({ key: entity, value: ledgers.length === 1 ? ledgers[0].id : "" });
      },
      () => undefined,
    );
    return () => controller.abort();
  }, [entity]);
  return derived.key === entity && entity !== "" ? derived.value : "";
}
