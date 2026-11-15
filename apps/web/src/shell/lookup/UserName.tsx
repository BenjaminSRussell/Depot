import { EntityName } from "./EntityName";

/** A user id shown as the user's name (the shell user lookup), never as the id. */
export function UserName({ id, fallback = "A user who is no longer listed" }: { id: string; fallback?: string }) {
  return <EntityName entity="user" id={id} fallback={fallback} />;
}
