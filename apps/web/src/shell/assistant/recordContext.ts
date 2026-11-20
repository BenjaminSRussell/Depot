import { matchPath, type RouteObject } from "react-router-dom";
import type { DepotModule } from "../../modules/registry";
import type { RecordContext } from "./session";

function patternsOf(routes: readonly RouteObject[], prefix = ""): string[] {
  return routes.flatMap((route) => {
    const own = route.path === undefined ? prefix : `${prefix}/${route.path}`.replace(/\/+/g, "/");
    return [own, ...patternsOf(route.children ?? [], own)];
  });
}

export function recordContextFor(pathname: string, modules: readonly DepotModule[]): RecordContext | undefined {
  for (const module of modules) {
    for (const pattern of patternsOf(module.routes)) {
      const match = pattern.includes(":") ? matchPath({ path: pattern, end: true }, pathname) : null;
      const id = Object.values(match?.params ?? {}).at(-1);
      if (id === undefined) continue;
      const segments = pattern.split("/").filter((part) => part !== "" && !part.startsWith(":"));
      return { kind: segments.at(-1) ?? module.id, id, path: pathname };
    }
  }
  return undefined;
}

export function recordRefOf(context: RecordContext): string {
  return `record:${context.kind}/${context.id}`;
}
