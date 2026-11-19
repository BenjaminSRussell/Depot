import { describe, expect, it } from "vitest";
import type { PreferenceStorage } from "../preferenceStorage";
import { addPin, MAX_PINS, moveAmong, pinKey, pinsKey, probeStorage, readPins, removePin, type PinTarget } from "./pinStore";

function memoryStorage(initial: Record<string, string> = {}, refuseWrites = false): PreferenceStorage & { data: Map<string, string> } {
  const data = new Map(Object.entries(initial));
  return {
    data,
    read: (key) => data.get(key) ?? null,
    write: (key, value) => {
      if (refuseWrites) return false;
      data.set(key, value);
      return true;
    },
    remove: (key) => void data.delete(key),
  };
}

const page = (route: string): PinTarget => ({ kind: "page", route });
const order = (id: string): PinTarget => ({ kind: "record", recordType: "purchase_order", id });
const NOW = () => new Date("2026-11-19T10:00:00.000Z");

function seeded(...targets: PinTarget[]) {
  const storage = memoryStorage();
  for (const t of targets) addPin("ann", t, storage, NOW);
  return storage;
}

describe("pin store", () => {
  it("adds pins in order and keeps only a route key, or a record type and id, with a timestamp", () => {
    const storage = seeded(page("/ledger/journal-batches"), order("po-1"));
    expect(readPins("ann", storage)).toEqual([
      { kind: "page", route: "/ledger/journal-batches", at: "2026-11-19T10:00:00.000Z" },
      { kind: "record", recordType: "purchase_order", id: "po-1", at: "2026-11-19T10:00:00.000Z" },
    ]);
    expect(Object.keys(JSON.parse(storage.data.get("depot.pins.v1.ann") ?? "[]")[1]).sort()).toEqual(["at", "id", "kind", "recordType"]);
  });

  it("pins the same page twice as one row", () => {
    const storage = seeded(page("/a"), page("/a"));
    expect(readPins("ann", storage)).toHaveLength(1);
  });

  it("refuses the 21st pin and drops nothing", () => {
    const storage = seeded(...Array.from({ length: MAX_PINS }, (_, n) => order(`po-${n}`)));
    const before = storage.data.get(pinsKey("ann"));
    expect(addPin("ann", order("one-more"), storage, NOW)).toEqual({ ok: false, reason: "limit" });
    expect(storage.data.get(pinsKey("ann"))).toBe(before);
    expect(readPins("ann", storage)).toHaveLength(MAX_PINS);
  });

  it("reports a refused write", () => {
    expect(addPin("ann", page("/a"), memoryStorage({}, true), NOW)).toEqual({ ok: false, reason: "storage" });
  });

  it("keeps each identity's pins apart and stores nothing for an empty identity", () => {
    const storage = memoryStorage();
    addPin("ann", page("/a"), storage, NOW);
    addPin("bob", page("/b"), storage, NOW);
    expect(readPins("ann", storage).map(pinKey)).toEqual(["page:/a"]);
    expect(readPins("bob", storage).map(pinKey)).toEqual(["page:/b"]);
    expect(addPin("", page("/c"), storage, NOW)).toEqual({ ok: false, reason: "storage" });
    expect([...storage.data.keys()].sort()).toEqual(["depot.pins.v1.ann", "depot.pins.v1.bob"]);
  });

  it("removes a pin, and removing one that is not there changes nothing", () => {
    const storage = seeded(page("/a"), page("/b"));
    expect(removePin("ann", "page:/a", storage)).toMatchObject({ ok: true, pins: [{ route: "/b" }] });
    expect(removePin("ann", "page:/zzz", storage)).toMatchObject({ ok: true, pins: [{ route: "/b" }] });
  });

  it("swaps a pin with its visible neighbour and leaves hidden pins where they are", () => {
    const storage = seeded(page("/a"), page("/hidden"), page("/b"), page("/c"));
    const visible = ["page:/a", "page:/b", "page:/c"];
    moveAmong("ann", "page:/b", visible, -1, storage);
    expect(readPins("ann", storage).map(pinKey)).toEqual(["page:/b", "page:/hidden", "page:/a", "page:/c"]);
  });

  it("leaves the list alone at the ends, for an unknown key and for an empty list", () => {
    const storage = seeded(page("/a"), page("/b"));
    const visible = ["page:/a", "page:/b"];
    moveAmong("ann", "page:/a", visible, -1, storage);
    moveAmong("ann", "page:/b", visible, 1, storage);
    moveAmong("ann", "page:/nope", visible, 1, storage);
    moveAmong("ann", "page:/a", [], 1, storage);
    expect(readPins("ann", storage).map(pinKey)).toEqual(["page:/a", "page:/b"]);
  });

  it.each([
    ["not JSON", "{oops"],
    ["not an array", '{"kind":"page"}'],
    ["empty", ""],
  ])("reads %s as no pins", (_name, raw) => {
    expect(readPins("ann", memoryStorage({ [pinsKey("ann")]: raw }))).toEqual([]);
  });

  it("keeps the good pins and drops wrong-typed, unknown, extra-key and badly dated items", () => {
    const at = "2026-11-19T10:00:00.000Z";
    const raw = JSON.stringify([
      { kind: "page", route: "/ok", at },
      { kind: "page", route: 7, at },
      { kind: "page", route: "/q?x=1", at },
      { kind: "record", recordType: "purchase_order", id: { x: 1 }, at },
      { kind: "record", recordType: "spaceship", id: "x", at },
      { kind: "record", recordType: "purchase_order", id: "has space", at },
      { kind: "page", route: "/extra", at, title: "Secret name" },
      { kind: "page", route: "/no-date" },
      { kind: "page", route: "/bad-date", at: "yesterday" },
      { kind: "prompt", text: "hello", at },
      null,
      "text",
      { kind: "record", recordType: "worker", id: "w-1", at },
    ]);
    expect(readPins("ann", memoryStorage({ [pinsKey("ann")]: raw })).map(pinKey)).toEqual(["page:/ok", "record:worker:w-1"]);
  });

  it("refuses to write a pin that does not fit the schema", () => {
    expect(addPin("ann", { kind: "page", route: "/search?q=1" }, memoryStorage(), NOW)).toEqual({ ok: false, reason: "storage" });
  });

  it("probes storage with a write, a read and a remove", () => {
    const storage = memoryStorage();
    expect(probeStorage(storage)).toBe(true);
    expect(storage.data.size).toBe(0);
    expect(probeStorage(memoryStorage({}, true))).toBe(false);
    expect(probeStorage({ read: () => "other", write: () => true, remove: () => undefined })).toBe(false);
  });
});
