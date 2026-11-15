// The Cedar Ridge Pumps staff the demo screens name as authors, approvers and requesters. Real services record
// user ids in those fields and the web app shows names through the shell user lookup, so the demo handlers hold
// ids too. userIdOf() returns the id of a person by display name: the identity users (finance/people.ts) keep
// their own ids, anyone else gets a stable id here, and the identity user-names handler resolves both.
import { uid } from "../store";
import { PEOPLE } from "./finance/people";

const K_DIRECTORY = 0xfd0;
const extra = new Map<string, string>();

const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** The id the demo holds for a person's display name. Ids, null and undefined pass through. */
export function userIdOf(name: string): string;
export function userIdOf(name: null): null;
export function userIdOf(name: undefined): undefined;
export function userIdOf(name: string | null): string | null;
export function userIdOf(name: string | undefined): string | undefined;
export function userIdOf(name: string | null | undefined): string | null | undefined;
export function userIdOf(name: string | null | undefined): string | null | undefined {
  if (name === null || name === undefined || UUID.test(name)) return name;
  const staff = PEOPLE.find((p) => p.name === name || p.username === name);
  if (staff) return staff.id;
  let id = extra.get(name);
  if (!id) {
    id = uid(K_DIRECTORY, extra.size + 1);
    extra.set(name, id);
  }
  return id;
}

/** The display name for an id the demo handed out, for the identity user-names handler. */
export function directoryName(id: string): string | undefined {
  for (const [name, known] of extra) if (known === id) return name;
  return undefined;
}
