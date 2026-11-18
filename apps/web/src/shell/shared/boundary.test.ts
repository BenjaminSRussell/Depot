// @vitest-environment node
import { parseAst } from "vite";

const sources = import.meta.glob<string>(["./**/*.{ts,tsx}", "!./**/*.test.{ts,tsx}"], { query: "?raw", import: "default", eager: true });

function importSpecifiers(source: string, file: string): string[] {
  const body = (parseAst(source, { lang: file.endsWith(".tsx") ? "tsx" : "ts" }, file) as unknown as { body: { type: string; source?: { value: string } | null }[] }).body;
  return body.flatMap((node) => (node.source ? [node.source.value] : []));
}

describe("shell/shared boundary", () => {
  it("imports nothing outside its own folder", () => {
    expect(Object.keys(sources).length).toBeGreaterThan(0);
    const outside = Object.entries(sources).flatMap(([file, source]) =>
      importSpecifiers(source, file)
        .filter((specifier) => !/^\.\/[^.]/.test(specifier))
        .map((specifier) => `${file} imports ${specifier}`),
    );
    expect(outside).toEqual([]);
  });

  it("flags a parent-folder import", () => {
    expect(importSpecifiers('import { x } from "../format";\nexport { y } from "react";', "bad.ts")).toEqual(["../format", "react"]);
  });
});
