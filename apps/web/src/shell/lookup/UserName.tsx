import { EntityName } from "./EntityName";

/**
 * A user id shown as the user's name (the shell user lookup), never as the id. An empty id shows `empty`
 * (default: a dash), an id that no longer resolves shows `fallback`.
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
  if (!id) {
    return <span>{empty}</span>;
  }
  return <EntityName entity="user" id={id} fallback={fallback} />;
}
