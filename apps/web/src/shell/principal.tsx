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
const IdentityContext = createContext("");

export function PrincipalProvider({ duties, identity = "", children }: { duties: readonly string[]; identity?: string; children: ReactNode }) {
  return (
    <PrincipalContext.Provider value={duties}>
      <IdentityContext.Provider value={identity}>{children}</IdentityContext.Provider>
    </PrincipalContext.Provider>
  );
}

export function usePrincipalIdentity(): string {
  return useContext(IdentityContext);
}

export function usePrincipalDuties(): readonly string[] {
  return useContext(PrincipalContext);
}
