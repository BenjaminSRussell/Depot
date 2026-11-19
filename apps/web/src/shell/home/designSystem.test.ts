import { describe, expect, it } from "vitest";

// @ts-expect-error TS2307: apps/web does not install node types; vitest runs on node
const { readdirSync, readFileSync } = (await import("node:fs")) as {
  readdirSync: (dir: URL) => string[];
  readFileSync: (file: URL, encoding: "utf8") => string;
};
const scanned = ["../pages/", "./"].map((dir) => new URL(dir, import.meta.url));
const sources: Record<string, string> = Object.fromEntries(
  scanned.flatMap((dir) =>
    readdirSync(dir)
      .filter((name) => /\.(?:tsx?|css)$/.test(name) && !/\.test\./.test(name))
      .map((name) => [`${dir.pathname}${name}`, readFileSync(new URL(name, dir), "utf8")] as const),
  ),
);

export const COLOR_LITERAL = /#[0-9a-fA-F]{3,8}\b|\b(?:rgb|rgba|hsl|hsla)\(/;
export const INLINE_STYLE_COLOR = /style=\{\{[^}]*\b(?:color|background|backgroundColor|borderColor|fill|stroke)\b/;
const ALLOWED_PACKAGES = new Set(["react", "react-router-dom", "zod"]);

export function foreignImports(source: string): string[] {
  const specifiers = [...source.matchAll(/^\s*(?:import|export)\b[^'"]*?from\s+["']([^"']+)["']|^\s*import\s+["']([^"']+)["']/gm)].map((m) => m[1] ?? m[2] ?? "");
  return specifiers.filter((s) => !s.startsWith(".") && !ALLOWED_PACKAGES.has(s));
}

describe("home design-system lint", () => {
  it("scans the page and home files", () => {
    const names = Object.keys(sources);
    expect(names.some((n) => n.endsWith("HomePage.tsx"))).toBe(true);
    expect(names.some((n) => n.endsWith("AssistantPrompt.tsx"))).toBe(true);
    expect(Object.entries(sources).find(([n]) => n.endsWith("home.module.css"))?.[1]).toContain("promptControl");
  });

  it("has no colour literal, foreign UI import or inline style colour", () => {
    const problems = Object.entries(sources).flatMap(([file, source]) => [
      ...(COLOR_LITERAL.test(source) ? [`${file}: colour literal`] : []),
      ...foreignImports(source).map((s) => `${file}: imports ${s}`),
      ...(INLINE_STYLE_COLOR.test(source) ? [`${file}: inline style colour`] : []),
    ]);
    expect(problems).toEqual([]);
  });

  it("recognises the shapes it looks for", () => {
    expect(COLOR_LITERAL.test("color: #fff;")).toBe(true);
    expect(COLOR_LITERAL.test("color: rgb(0 0 0);")).toBe(true);
    expect(COLOR_LITERAL.test("color: hsl(1, 2%, 3%);")).toBe(true);
    expect(COLOR_LITERAL.test("color: var(--color-text); id=\"#main\"")).toBe(false);
    expect(foreignImports('import { Button } from "@mui/material";\nimport { Link } from "react-router-dom";\nimport x from "../ui";')).toEqual(["@mui/material"]);
    expect(INLINE_STYLE_COLOR.test('<p style={{ color: "red" }} />')).toBe(true);
    expect(INLINE_STYLE_COLOR.test("<p style={{ inlineSize: 3 }} />")).toBe(false);
  });
});
