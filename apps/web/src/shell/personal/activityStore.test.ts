import { describe, expect, it } from "vitest";
import type { PinTarget } from "../pins/pinStore";
import { noPreferenceStorage } from "../preferenceStorage";
import { memoryStorage } from "../testing/onboardingFixtures";
import { activityKey, MAX_ACTIVITY, readActivity, recordActivity } from "./activityStore";

const NOW = () => new Date("2026-11-19T10:00:00.000Z");
const page = (route: string): PinTarget => ({ kind: "page", route });
const order = (id: string): PinTarget => ({ kind: "record", recordType: "purchase_order", id });
const storedRows = (storage: ReturnType<typeof memoryStorage>): Record<string, unknown>[] => JSON.parse(storage.entries.get(activityKey("ann")) ?? "[]");

describe("activity store", () => {
  it("keeps only an entity type, a route or record id, and a timestamp, newest first", () => {
    const storage = memoryStorage();
    recordActivity("ann", page("/party"), storage, NOW);
    recordActivity("ann", order("po-1"), storage, NOW);
    expect(storedRows(storage)).toEqual([
      { kind: "record", recordType: "purchase_order", id: "po-1", at: "2026-11-19T10:00:00.000Z" },
      { kind: "page", route: "/party", at: "2026-11-19T10:00:00.000Z" },
    ]);
    expect(Object.keys(storedRows(storage)[0] ?? {}).sort()).toEqual(["at", "id", "kind", "recordType"]);
    expect([...storage.entries.keys()]).toEqual(["depot.activity.v1.ann"]);
  });

  it("moves a repeated target to the front without duplicating it", () => {
    const storage = memoryStorage();
    recordActivity("ann", page("/a"), storage, NOW);
    recordActivity("ann", page("/b"), storage, NOW);
    recordActivity("ann", page("/a"), storage, NOW);
    expect(readActivity("ann", storage).map((row) => (row.kind === "page" ? row.route : row.id))).toEqual(["/a", "/b"]);
  });

  it("drops the oldest row past the cap", () => {
    const storage = memoryStorage();
    for (let n = 0; n <= MAX_ACTIVITY; n += 1) recordActivity("ann", order(`po-${n}`), storage, NOW);
    const rows = readActivity("ann", storage);
    expect(rows).toHaveLength(MAX_ACTIVITY);
    expect(rows.some((row) => row.kind === "record" && row.id === "po-0")).toBe(false);
  });

  it("writes and reads nothing for an empty identity", () => {
    const storage = memoryStorage({ [activityKey("")]: JSON.stringify([{ kind: "page", route: "/a", at: "2026-11-19T10:00:00.000Z" }]) });
    recordActivity("", page("/b"), storage, NOW);
    expect(readActivity("", storage)).toEqual([]);
    expect(storage.entries.size).toBe(1);
  });

  it("does not throw when the browser refuses storage", () => {
    expect(() => recordActivity("ann", page("/a"), noPreferenceStorage, NOW)).not.toThrow();
    expect(readActivity("ann", noPreferenceStorage)).toEqual([]);
  });

  it.each([["not json", "{"], ["empty text", ""], ["an object", '{"kind":"page"}'], ["a string", '"x"'], ["a number", "3"]])("reads %s as no activity", (_name, value) => {
    expect(readActivity("ann", memoryStorage({ [activityKey("ann")]: value }))).toEqual([]);
  });

  it("skips rows that are wrong-typed, carry a name or label, or hold a query, and keeps the good rows", () => {
    const good = { kind: "record", recordType: "party", id: "p-1", at: "2026-11-19T10:00:00.000Z" };
    const raw = JSON.stringify([
      null,
      3,
      "x",
      { ...good, name: "Acme Corp" },
      { ...good, label: "Acme" },
      { ...good, id: 7 },
      { ...good, at: { when: "now" } },
      { ...good, at: "yesterday" },
      { kind: "page", route: "/party?q=acme", at: "2026-11-19T10:00:00.000Z" },
      good,
    ]);
    expect(readActivity("ann", memoryStorage({ [activityKey("ann")]: raw }))).toEqual([good]);
  });
});
