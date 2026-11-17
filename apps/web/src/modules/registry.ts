import type { RouteObject } from "react-router-dom";

/** What every module exports from `modules/<name>/module.ts`. */
export interface NavItem {
  label: string;
  to: string;
  /** Duty the user must hold to see the entry; omitted = visible to all. */
  duty?: string;
}

export interface DepotModule {
  /** Stable id, equal to the folder name. */
  id: string;
  title: string;
  description?: string;
  routes: RouteObject[];
  nav: NavItem[];
}

/** Auto-discovers every module. Modules never register themselves anywhere else. */
export function loadModules(): DepotModule[] {
  const found = import.meta.glob<{ default: DepotModule }>("./*/module.ts", { eager: true });
  return Object.values(found)
    .map((m) => m.default)
    .sort((a, b) => a.title.localeCompare(b.title));
}
