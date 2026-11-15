import { useEffect, useState } from "react";
import { formatOption, isOptionMissing, resolveOption } from "./entities";

type Shown = { kind: "loading" } | { kind: "name"; text: string } | { kind: "missing" } | { kind: "unavailable" };

/** "service:intercompany-charges" and "system:finreport-sweep" are background actors, not users: "System (intercompany-charges)". */
export function systemActorLabel(id: string): string | null {
  const match = /^(service|system):(.+)$/i.exec(id.trim());
  return match ? `System (${match[2]})` : null;
}

/**
 * A user id shown as the user's name through the shell user lookup, never as the id. It is plain inline text (no "Copy id" menu, which
 * EntityName adds), so it can sit inside a sentence, a paragraph or a table cell. While the name loads it shows a neutral placeholder;
 * `fallback` appears only when the server confirmed the user does not exist, "Not available" when the lookup failed, and an empty id
 * shows `empty` (default: a dash). Background actors (`service:<name>`) read "System (<name>)". Lookups on one page go out as one
 * request.
 */
export function UserName({
  id,
  empty = "—",
  fallback = "A user who is no longer listed",
}: {
  id?: string | null;
  empty?: string;
  fallback?: string;
}) {
  const [shown, setShown] = useState<Shown>({ kind: "loading" });
  const system = id ? systemActorLabel(id) : null;

  useEffect(() => {
    let live = true;
    setShown({ kind: "loading" });
    if (id && !system) {
      void resolveOption("user", id).then(async (found) => {
        if (!live) return;
        if (found) {
          setShown({ kind: "name", text: formatOption(found) });
          return;
        }
        const missing = await isOptionMissing("user", id);
        if (live) setShown({ kind: missing ? "missing" : "unavailable" });
      });
    }
    return () => {
      live = false;
    };
  }, [id, system]);

  if (!id) return <span>{empty}</span>;
  if (system) return <span>{system}</span>;
  switch (shown.kind) {
    case "name":
      return <span>{shown.text}</span>;
    case "missing":
      return <span>{fallback}</span>;
    case "unavailable":
      return <span>Not available</span>;
    default:
      return <span aria-busy="true">{"…"}</span>;
  }
}
