import { describe, expect, it } from "vitest";
import type { PaletteEntry } from "../CommandPalette";
import { buildSections, NAVIGATE_CAP, rankNavigate } from "./paletteResults";
import type { RecordHit } from "./records";

const entry = (label: string, group = "Admin", keywords?: string): PaletteEntry => ({ id: label, group, label, to: `/${label}`, keywords });
const hit: RecordHit = { type: "party", id: "v-1", number: "V-0007", title: "Contoso", link: "/party/v-1" };
const base = { entries: [entry("Vendors")], hits: [], recents: [], favourites: [], query: "", assistant: true };

describe("rankNavigate", () => {
  it("orders exact, prefix, word start, substring, then group and keyword matches, keeping ties in order", () => {
    const entries = [entry("Old vendor list", "Purchasing"), entry("Setup", "Vendors"), entry("Vendor", "X"), entry("Vendors payable", "X"), entry("Re-vendor", "X"), entry("Other", "X", "vendor")];
    expect(rankNavigate(entries, "vendor").map((e) => e.label)).toEqual(["Vendor", "Vendors payable", "Old vendor list", "Re-vendor", "Setup", "Other"]);
  });

  it("drops entries that do not match and returns everything for an empty query", () => {
    const entries = [entry("Vendors"), entry("Tax")];
    expect(rankNavigate(entries, "tax")).toEqual([entries[1]]);
    expect(rankNavigate(entries, "  ")).toEqual(entries);
  });
});

describe("buildSections", () => {
  it("keeps every entry uncapped on an empty query and adds no assistant row", () => {
    const entries = Array.from({ length: 9 }, (_, i) => entry(`Page ${i}`));
    const sections = buildSections({ ...base, entries });
    expect(sections.map((s) => s.id)).toEqual(["navigate"]);
    expect(sections[0].items).toHaveLength(9);
  });

  it("lists favourites, then recents without the favourites, then Navigate for an empty query", () => {
    const other: RecordHit = { ...hit, id: "v-2", number: "V-0008" };
    const sections = buildSections({ ...base, favourites: [hit], recents: [hit, other] });
    expect(sections.map((s) => [s.id, s.items.length])).toEqual([["favourites", 1], ["recent", 1], ["navigate", 1]]);
  });

  it("orders Navigate, Records, Assistant and caps Navigate while searching", () => {
    const entries = Array.from({ length: NAVIGATE_CAP + 3 }, (_, i) => entry(`Vendor ${i}`));
    const sections = buildSections({ ...base, entries, hits: [hit], query: "vendor" });
    expect(sections.map((s) => s.id)).toEqual(["navigate", "records", "assistant"]);
    expect(sections[0].items).toHaveLength(NAVIGATE_CAP);
    expect(sections[1].items[0].record).toBe(hit);
  });

  it("keeps the assistant row alone when nothing matches", () => {
    const sections = buildSections({ ...base, query: "zzqq" });
    expect(sections.map((s) => s.id)).toEqual(["assistant"]);
    expect(sections[0].items[0]).toMatchObject({ ask: true, label: "Ask the assistant: zzqq" });
  });

  it("has no assistant section when the assistant is off, and none for a whitespace query", () => {
    expect(buildSections({ ...base, query: "zzqq", assistant: false })).toEqual([]);
    expect(buildSections({ ...base, query: "   " }).map((s) => s.id)).toEqual(["navigate"]);
  });
});
