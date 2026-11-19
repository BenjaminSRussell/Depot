import { describe, expect, it } from "vitest";
import allowlist from "./rawIdAllowlist.json";

const RAW_ID_INPUT = /<(?:Input|Textarea|input)\b(?:(?!\/>|<\/)[\s\S]){0,400}?\bvalue=\{[^}\n]*?\b(\w*Ids?)\b[^}\n]*\}/g;
const KEEPS_IDS = /^modules\/(admin|integration|migration|migration-factory)\//;

const sources = import.meta.glob<string>(["../../**/*.tsx", "!../../**/*.test.tsx", "!../../shell/lookup/**"], {
  query: "?raw",
  import: "default",
  eager: true,
});

function rawIdFields(source: string): string[] {
  return [...new Set([...source.matchAll(RAW_ID_INPUT)].map((match) => match[1]))].sort();
}

function found(): Record<string, string[]> {
  const files: Record<string, string[]> = {};
  for (const [path, source] of Object.entries(sources)) {
    const fields = rawIdFields(source);
    if (fields.length > 0) files[path.replace("../../", "")] = fields;
  }
  return files;
}

const listed: Record<string, string[]> = { ...allowlist.permanent.files, ...allowlist.pending.files };

describe("raw id inputs", () => {
  it("has no text input bound to an id field outside the allowlist", () => {
    const unlisted = Object.entries(found()).filter(([file, fields]) => fields.some((field) => !listed[file]?.includes(field)));
    expect(unlisted.map(([file, fields]) => `${file}: ${fields.join(", ")}`)).toEqual([]);
  });

  it("flags an input bound to an id however its label reads, and leaves pickers and names alone", () => {
    const flagged = [
      '<Input {...a11y} value={draft.siteId} onChange={(e) => set("siteId")(e.target.value)} />',
      '<Textarea\n  rows={3}\n  value={form.itemIds ?? ""}\n/>',
      '<input value={legalEntityId} />',
    ];
    expect(flagged.map((sample) => rawIdFields(sample))).toEqual([["siteId"], ["itemIds"], ["legalEntityId"]]);
    const clean = [
      '<EntityLookup entity="site" value={draft.siteId} onChange={pick} />',
      '<EntityMultiLookup entity="product" value={draft.scopeItemIds} onChange={pick} />',
      '<Input value={draft.name} />\n<EntityLookup value={draft.siteId} />',
      '<Input {...a11y} value={row.id} />',
    ];
    expect(clean.map((sample) => rawIdFields(sample))).toEqual([[], [], [], []]);
  });

  it("lists only inputs that still exist, so the allowlist shrinks as screens are converted", () => {
    const current = found();
    const stale = Object.entries(listed).filter(([file, fields]) => JSON.stringify(fields) !== JSON.stringify(current[file] ?? []));
    expect(stale.map(([file]) => file)).toEqual([]);
  });

  it("keeps ids only on admin, integration and migration screens, apart from the screens still to convert", () => {
    expect(Object.keys(allowlist.permanent.files).filter((file) => !KEEPS_IDS.test(file))).toEqual([]);
    expect(Object.keys(allowlist.pending.files).filter((file) => KEEPS_IDS.test(file))).toEqual([]);
  });

  it("gives each allowlist group a reason", () => {
    expect([allowlist.permanent.reason, allowlist.pending.reason].filter((reason) => reason.trim() === "")).toEqual([]);
  });
});
