import { describe, expect, it } from "vitest";
import type { PreferenceStorage } from "../preferenceStorage";
import { createAssistantStore } from "./assistantStorage";

function memoryStorage() {
  const data = new Map<string, string>();
  const storage: PreferenceStorage = {
    read: (key) => data.get(key) ?? null,
    write: (key, value) => {
      data.set(key, value);
      return true;
    },
    remove: (key) => {
      data.delete(key);
    },
  };
  return { data, storage };
}

const AT = "2026-11-18T10:00:00Z";
const pin = { id: "ord-1", at: AT };
const activity = { id: "ord-1", route: "/sales/orders/ord-1", at: AT, count: 2 };

describe("assistant storage", () => {
  it("keeps ids, route keys, timestamps and counts", () => {
    const { data, storage } = memoryStorage();
    const store = createAssistantStore(storage);
    expect(store.write("pins", "u1", [pin])).toEqual({ ok: true });
    expect(store.write("activity", "u1", [activity])).toEqual({ ok: true });
    expect(store.read("pins", "u1")).toEqual([pin]);
    expect(store.read("activity", "u1")).toEqual([activity]);
    expect([...data.keys()].sort()).toEqual(["depot.assistant.activity.v1.u1", "depot.assistant.pins.v1.u1"]);
  });

  it.each([
    ["an extra field", [{ ...pin, text: "how is cash" }]],
    ["a sentence as an id", [{ ...pin, id: "how is cash" }]],
    ["an empty id", [{ ...pin, id: "" }]],
    ["a 129 character id", [{ ...pin, id: "a".repeat(129) }]],
    ["a newline in an id", [{ ...pin, id: "a\nb" }]],
    ["a malformed timestamp", [{ ...pin, at: "2026-13-40T00:00:00Z" }]],
    ["a numeric timestamp", [{ ...pin, at: 1 }]],
    ["an object instead of a list", pin],
    ["a string instead of a list", "x"],
    ["null", null],
  ])("refuses %s and stores nothing", (_name, rows) => {
    const { data, storage } = memoryStorage();
    expect(createAssistantStore(storage).write("pins", "u1", rows)).toEqual({ ok: false, reason: "schema" });
    expect(data.size).toBe(0);
  });

  it.each([
    ["a negative count", { ...activity, count: -1 }],
    ["a fractional count", { ...activity, count: 1.5 }],
    ["a text count", { ...activity, count: "3" }],
    ["a NaN count", { ...activity, count: Number.NaN }],
    ["an infinite count", { ...activity, count: Number.POSITIVE_INFINITY }],
    ["a route with a query", { ...activity, route: "/sales?q=cash" }],
    ["a route with a space", { ...activity, route: "/how is cash" }],
    ["a route without a leading slash", { ...activity, route: "sales" }],
  ])("refuses activity with %s", (_name, row) => {
    const { data, storage } = memoryStorage();
    expect(createAssistantStore(storage).write("activity", "u1", [row])).toEqual({ ok: false, reason: "schema" });
    expect(data.size).toBe(0);
  });

  it("stores an empty list", () => {
    const store = createAssistantStore(memoryStorage().storage);
    expect(store.write("pins", "u1", [])).toEqual({ ok: true });
    expect(store.read("pins", "u1")).toEqual([]);
  });

  it("writes nothing without a signed-in identity", () => {
    const { data, storage } = memoryStorage();
    expect(createAssistantStore(storage).write("pins", "", [pin])).toEqual({ ok: false, reason: "no-identity" });
    expect(data.size).toBe(0);
  });

  it.each([["absent", null], ["empty", ""], ["null", "null"], ["an empty object", "{}"], ["corrupt", "{not json"], ["an older row with an extra field", JSON.stringify([{ ...pin, label: "Acme" }])]])(
    "reads %s content as an empty list",
    (_name, stored) => {
      const { data, storage } = memoryStorage();
      if (stored !== null) data.set("depot.assistant.pins.v1.u1", stored);
      expect(createAssistantStore(storage).read("pins", "u1")).toEqual([]);
    },
  );

  it("keeps working from memory when the store refuses or throws", () => {
    const refusing = createAssistantStore({ read: () => null, write: () => false, remove: () => undefined });
    expect(refusing.write("pins", "u1", [pin])).toEqual({ ok: true });
    expect(refusing.read("pins", "u1")).toEqual([pin]);
    const throwing = createAssistantStore({
      read: () => {
        throw new Error("denied");
      },
      write: () => {
        throw new Error("denied");
      },
      remove: () => undefined,
    });
    expect(throwing.write("pins", "u1", [pin])).toEqual({ ok: true });
    expect(throwing.read("pins", "u1")).toEqual([pin]);
  });
});
