import { z } from "zod";
import { localPreferenceStorage, type PreferenceStorage } from "../preferenceStorage";
import { RECORD_TYPES } from "../search/records";

export const MAX_PINS = 20;

const at = z.iso.datetime();
const PinSchema = z.discriminatedUnion("kind", [
  z.strictObject({ kind: z.literal("page"), route: z.string().regex(/^\/[A-Za-z0-9_\-./:]{0,199}$/), at }),
  z.strictObject({ kind: z.literal("record"), recordType: z.enum(RECORD_TYPES), id: z.string().regex(/^[A-Za-z0-9_.:-]{1,128}$/), at }),
]);
const PinList = z.array(PinSchema).max(MAX_PINS);

export type Pin = z.infer<typeof PinSchema>;
export type PinTarget = { kind: "page"; route: string } | { kind: "record"; recordType: (typeof RECORD_TYPES)[number]; id: string };
export type PinChange = { ok: true; pins: Pin[] } | { ok: false; reason: "limit" | "storage" };

const PROBE_KEY = "depot.pins.probe";

export const pinsKey = (identity: string) => `depot.pins.v1.${identity}`;

export const pinKey = (pin: PinTarget): string => (pin.kind === "page" ? `page:${pin.route}` : `record:${pin.recordType}:${pin.id}`);

export function probeStorage(storage: PreferenceStorage = localPreferenceStorage): boolean {
  const ok = storage.write(PROBE_KEY, "1") && storage.read(PROBE_KEY) === "1";
  storage.remove(PROBE_KEY);
  return ok;
}

export function readPins(identity: string, storage: PreferenceStorage = localPreferenceStorage): Pin[] {
  if (identity === "") return [];
  let parsed: unknown;
  try {
    parsed = JSON.parse(storage.read(pinsKey(identity)) ?? "[]");
  } catch {
    return [];
  }
  if (!Array.isArray(parsed)) return [];
  return parsed.flatMap((item) => {
    const result = PinSchema.safeParse(item);
    return result.success ? [result.data] : [];
  }).slice(0, MAX_PINS);
}

function commit(identity: string, pins: Pin[], storage: PreferenceStorage): PinChange {
  const valid = PinList.safeParse(pins);
  return identity !== "" && valid.success && storage.write(pinsKey(identity), JSON.stringify(valid.data)) ? { ok: true, pins: valid.data } : { ok: false, reason: "storage" };
}

export function addPin(identity: string, target: PinTarget, storage: PreferenceStorage = localPreferenceStorage, now: () => Date = () => new Date()): PinChange {
  const pins = readPins(identity, storage);
  if (pins.some((p) => pinKey(p) === pinKey(target))) return { ok: true, pins };
  if (pins.length >= MAX_PINS) return { ok: false, reason: "limit" };
  return commit(identity, [...pins, { ...target, at: now().toISOString() }], storage);
}

export function removePin(identity: string, key: string, storage: PreferenceStorage = localPreferenceStorage): PinChange {
  return commit(identity, readPins(identity, storage).filter((p) => pinKey(p) !== key), storage);
}

/** Swaps a pin with its neighbour among the pins on show, so hidden pins keep their place. */
export function moveAmong(identity: string, key: string, visible: readonly string[], direction: -1 | 1, storage: PreferenceStorage = localPreferenceStorage): PinChange {
  const pins = readPins(identity, storage);
  const neighbour = visible[visible.indexOf(key) + direction];
  const from = pins.findIndex((p) => pinKey(p) === key);
  const to = pins.findIndex((p) => pinKey(p) === neighbour);
  if (visible.indexOf(key) < 0 || from < 0 || to < 0) return { ok: true, pins };
  const next = [...pins];
  [next[from], next[to]] = [pins[to] as Pin, pins[from] as Pin];
  return commit(identity, next, storage);
}
