import { createContext, useContext, type ReactNode } from "react";

declare const __DEPOT_DEV_DUTIES__: string | undefined;
declare const __DEPOT_DEV_IDENTITY__: string | undefined;

export function parseDuties(raw: string | undefined): string[] {
  return (raw ?? "")
    .split(",")
    .map((duty) => duty.trim())
    .filter((duty) => duty !== "");
}

export function devDuties(): string[] {
  return parseDuties(typeof __DEPOT_DEV_DUTIES__ === "string" ? __DEPOT_DEV_DUTIES__ : undefined);
}

export function devIdentity(): string {
  return typeof __DEPOT_DEV_IDENTITY__ === "string" ? __DEPOT_DEV_IDENTITY__ : "";
}

const PrincipalContext = createContext<readonly string[]>([]);

export function PrincipalProvider({ duties, children }: { duties: readonly string[]; children: ReactNode }) {
  return <PrincipalContext.Provider value={duties}>{children}</PrincipalContext.Provider>;
}

export function usePrincipalDuties(): readonly string[] {
  return useContext(PrincipalContext);
}
