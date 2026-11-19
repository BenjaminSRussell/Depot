import { describe, expect, it } from "vitest";
import allowlist from "./rawIdColumnAllowlist.json";

const RAW_ID_COLUMN = /\{\s*key:\s*"(\w*Ids?)"([^{}]*?)\}/g;
const KEEPS_IDS = /^modules\/(admin|integration|migration|migration-factory)\//;

const sources = import.meta.glob<string>(["../../**/*.tsx", "!../../**/*.test.tsx", "!../../shell/lookup/**"], {
  query: "?raw",
  import: "default",
  eager: true,
});

function rawIdColumns(source: string): string[] {
  return [...new Set([...source.matchAll(RAW_ID_COLUMN)].filter((match) => !/\brender\b/.test(match[2])).map((match) => match[1]))].sort();
}

function found(): Record<string, string[]> {
  const files: Record<string, string[]> = {};
  for (const [path, source] of Object.entries(sources)) {
    const columns = rawIdColumns(source);
    if (columns.length > 0) files[path.replace("../../", "")] = columns;
  }
  return files;
}

const listed: Record<string, string[]> = { ...allowlist.permanent.files, ...allowlist.pending.files };

describe("id columns", () => {
  it("has no table column that prints an id outside the allowlist", () => {
    const unlisted = Object.entries(found()).filter(([file, columns]) => columns.some((column) => !listed[file]?.includes(column)));
    expect(unlisted.map(([file, columns]) => `${file}: ${columns.join(", ")}`)).toEqual([]);
  });

  it("flags a column with no renderer and accepts one that shows a name", () => {
    expect(rawIdColumns('{ key: "vendorId", header: "Vendor", mono: true }')).toEqual(["vendorId"]);
    expect(rawIdColumns('{ key: "siteId", header: "Site", render: (row) => <EntityName entity="site" id={row.siteId} /> }')).toEqual([]);
    expect(rawIdColumns('{ key: "name", header: "Name" }')).toEqual([]);
  });

  it("lists only columns that still exist, so the allowlist shrinks as tables are converted", () => {
    const current = found();
    const stale = Object.entries(listed).filter(([file, columns]) => JSON.stringify(columns) !== JSON.stringify(current[file] ?? []));
    expect(stale.map(([file]) => file)).toEqual([]);
  });

  it("keeps ids only on admin, integration and migration screens, apart from the tables still to convert", () => {
    expect(Object.keys(allowlist.permanent.files).filter((file) => !KEEPS_IDS.test(file))).toEqual([]);
    expect(Object.keys(allowlist.pending.files).filter((file) => KEEPS_IDS.test(file))).toEqual([]);
  });

  it("gives each allowlist group a reason", () => {
    expect([allowlist.permanent.reason, allowlist.pending.reason].filter((reason) => reason.trim() === "")).toEqual([]);
  });
});
