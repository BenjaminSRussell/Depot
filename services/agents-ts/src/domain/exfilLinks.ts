import { PSEUDONYM_RE } from "./redaction.js";

export const LINK_REMOVED = "[link removed]";

const SCAN_CAP = 2048;
const CLOSE_CAP = 256;
const BLOCKED_TAGS: ReadonlySet<string> = new Set([
  "img", "a", "iframe", "script", "link", "meta", "object", "embed", "form", "svg",
  "image", "video", "audio", "source", "track", "input", "button", "base", "area", "use", "frame", "frameset", "applet", "style",
]);
const TAG_NAME = /<\/?([A-Za-z][A-Za-z0-9]*)/y;
const AUTOLINK = /<[A-Za-z][A-Za-z0-9+.-]{1,31}:[^\s<>]*>/y;
const SCHEME_URL = /[A-Za-z][A-Za-z0-9+.-]*:\/\/\S/y;
const WWW_HOST = /www\./iy;
const OPAQUE_URI = /(?:data|javascript|vbscript|file):(?=\S)/iy;
const DEFINITION = /[ \t>]*(?:(?:[-*+]|\d{1,9}[.)])[ \t]+)?(\[[^\]\n]*\]):[ \t]*(<[^>\n]*>|\S*)/y;
const PSEUDONYM_LABEL = new RegExp(`^${PSEUDONYM_RE.source}$`);
const WORD_CHAR = /[A-Za-z0-9+.-]/;

export function isAllowedDestination(dest: string): boolean {
  if (dest.length < 2 || dest[0] !== "/" || dest[1] === "/" || dest.includes("//")) return false;
  for (const ch of dest) {
    const code = ch.charCodeAt(0);
    if (code <= 32 || code === 127 || ch === "\\" || ch === "&") return false;
  }
  return true;
}

const isBlank = (ch: string | undefined): boolean => ch === " " || ch === "\t" || ch === "\n" || ch === "\r";

function readDestination(text: string, from: number): { dest: string; end: number } {
  let start = from;
  while (start < text.length && isBlank(text[start])) start += 1;
  const stop = Math.min(text.length, start + SCAN_CAP);
  if (text[start] === "<") {
    let j = start + 1;
    while (j < stop && text[j] !== ">" && text[j] !== "\n") j += 1;
    return text[j] === ">" ? { dest: text.slice(start + 1, j), end: j + 1 } : { dest: text.slice(start, j), end: j };
  }
  let depth = 0;
  let j = start;
  while (j < stop && !isBlank(text[j])) {
    if (text[j] === "(") depth += 1;
    else if (text[j] === ")") {
      if (depth === 0) break;
      depth -= 1;
    }
    j += 1;
  }
  return { dest: j === stop && stop - start >= SCAN_CAP ? `${text.slice(start, j)}\\` : text.slice(start, j), end: j };
}

function closeParen(text: string, from: number): number {
  const stop = Math.min(text.length, from + CLOSE_CAP);
  for (let j = from; j < stop && text[j] !== "\n"; j += 1) if (text[j] === ")") return j + 1;
  return from;
}

function runEnd(text: string, from: number): number {
  let depth = 0;
  let j = from;
  while (j < text.length) {
    const ch = text[j] as string;
    if (isBlank(ch) || ch === "<" || ch === ">" || ch === '"' || ch === "'" || ch === "]") break;
    if (ch === "(") depth += 1;
    else if (ch === ")") {
      if (depth === 0) break;
      depth -= 1;
    }
    j += 1;
  }
  return j;
}

function matches(re: RegExp, text: string, at: number): RegExpExecArray | null {
  re.lastIndex = at;
  return re.exec(text);
}

function definitionEnd(text: string, at: number): number {
  const found = matches(DEFINITION, text, at);
  if (found === null || PSEUDONYM_LABEL.test(found[1] ?? "") || isAllowedDestination((found[2] ?? "").replace(/^<|>$/g, ""))) return at;
  const newline = text.indexOf("\n", at);
  return newline === -1 ? text.length : newline;
}

function tagEnd(text: string, at: number): number {
  const found = matches(TAG_NAME, text, at);
  if (found === null || !BLOCKED_TAGS.has((found[1] ?? "").toLowerCase())) return -1;
  const stop = Math.min(text.length, at + SCAN_CAP);
  for (let j = at + 1; j < stop; j += 1) if (text[j] === ">") return j + 1;
  return at + 1;
}

export function neutraliseLinks(text: string): { text: string; removed: number } {
  const out: string[] = [];
  const open: number[] = [];
  let removed = 0;
  let i = 0;
  while (i < text.length) {
    const c = text[i] as string;
    if (i === 0 || text[i - 1] === "\n") {
      const skip = definitionEnd(text, i);
      if (skip > i) {
        removed += 1;
        i = skip;
        continue;
      }
    }
    if (c === "[") {
      open.push(out.length);
      out.push(c);
      i += 1;
    } else if (c === "]") {
      const from = open.pop();
      const inline = text[i + 1] === "(" ? readDestination(text, i + 2) : undefined;
      const image = from !== undefined && out[from - 1] === "!";
      if (inline !== undefined && (image || !isAllowedDestination(inline.dest))) {
        removed += 1;
        if (from !== undefined) {
          if (image) out.length = from - 1;
          else out[from] = "";
        }
        i = closeParen(text, inline.end);
      } else {
        out.push(c);
        i += 1;
      }
    } else if (c === "<") {
      const end = tagEnd(text, i);
      const auto = end === -1 ? matches(AUTOLINK, text, i) : null;
      if (end !== -1) {
        removed += 1;
        i = end;
      } else if (auto !== null) {
        removed += 1;
        out.push(LINK_REMOVED);
        i += auto[0].length;
      } else {
        out.push(c);
        i += 1;
      }
    } else if (/[A-Za-z]/.test(c) && (i === 0 || !WORD_CHAR.test(text[i - 1] as string)) && [SCHEME_URL, WWW_HOST, OPAQUE_URI].some((re) => matches(re, text, i) !== null)) {
      removed += 1;
      out.push(LINK_REMOVED);
      i = Math.max(i + 1, runEnd(text, i));
    } else {
      out.push(c);
      i += 1;
    }
  }
  return { text: out.join(""), removed };
}

export function unsafeTail(text: string): number {
  const open: number[] = [];
  let tag = -1;
  let hold = text.length;
  for (let i = 0; i < text.length; i += 1) {
    const c = text[i];
    if (c === "\n" && text[i + 1] === "\n") {
      open.length = 0;
      tag = -1;
    } else if (c === "[") open.push(i);
    else if (c === "<") tag = tag === -1 ? i : tag;
    else if (c === ">") tag = -1;
    else if (c === "]") {
      const from = open.pop() ?? i;
      const unresolved = i === text.length - 1 || (text[i + 1] === "(" && !text.slice(i + 2, i + 2 + SCAN_CAP).includes(")"));
      if (unresolved) hold = Math.min(hold, from);
    }
  }
  hold = Math.min(hold, open[0] ?? hold, tag === -1 ? hold : tag);
  if (text.endsWith("!")) hold = Math.min(hold, text.length - 1);
  return hold > 0 && text[hold] === "[" && text[hold - 1] === "!" ? hold - 1 : hold;
}
