import { useEffect, useState } from "react";
import { formatOption, resolveOption } from "./entities";

/**
 * A user id shown as the user's name through the shell user lookup, never as the id. It is plain inline text (no "Copy id" menu, which
 * EntityName adds), so it can sit inside a sentence, a paragraph or a table cell. An empty id shows `empty` (default: a dash), an id
 * that does not resolve, or whose lookup fails, shows `fallback`.
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
  const [name, setName] = useState<string | null>(null);

  useEffect(() => {
    let live = true;
    setName(null);
    if (id) {
      void resolveOption("user", id).then((found) => {
        if (live) setName(found ? formatOption(found) : null);
      });
    }
    return () => {
      live = false;
    };
  }, [id]);

  if (!id) return <span>{empty}</span>;
  return <span>{name ?? fallback}</span>;
}
