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
const PSEUDONYM_LABEL = new RegExp(`^${PSEUDONYM_RE.source}$`);
const WORD_CHAR = /[A-Za-z0-9+.-]/;
const PROTOCOL_RELATIVE = /\/\/[^\s/\\]/y;
const LABEL_CAP = 999;

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

const normalised = (label: string): string => label.trim().replace(/\s+/g, " ").toLowerCase();
const isTokenLabel = (inner: string): boolean => PSEUDONYM_LABEL.test(`[${inner}]`);

function prefixLength(line: string): number {
  let i = 0;
  for (;;) {
    while (i < line.length && (/\s/.test(line[i] as string) || line[i] === ">")) i += 1;
    const marker = /^(?:[-*+]|\d{1,9}[.)])(?=\s|$)/.exec(line.slice(i, i + 12));
    if (marker === null) return i;
    i += marker[0].length;
  }
}

function labelEnd(s: string, from: number): number {
  for (let j = from; j < s.length; j += 1) {
    if (s[j] === "\\") j += 1;
    else if (s[j] === "[") return -1;
    else if (s[j] === "]") return s[j + 1] === ":" ? j : -1;
  }
  return -1;
}

function bracketBalance(s: string): number {
  let balance = 0;
  for (let j = 0; j < s.length; j += 1) {
    if (s[j] === "\\") j += 1;
    else if (s[j] === "[") balance += 1;
    else if (s[j] === "]") balance -= 1;
  }
  return balance;
}

function destinationOf(rest: string): string {
  const t = rest.trimStart();
  if (t.startsWith("<")) {
    const close = t.indexOf(">");
    return close === -1 ? t : t.slice(1, close);
  }
  return t.split(/\s/, 1)[0] ?? "";
}

function dropDefinitions(text: string): { text: string; removed: number; labels: Set<string> } {
  const lines = text.split("\n");
  const labels = new Set<string>();
  let removed = 0;
  let depth = 0;
  for (let n = 0; n < lines.length; n += 1) {
    const line = lines[n] as string;
    const s = line.slice(prefixLength(line));
    if (s === "") {
      depth = 0;
      continue;
    }
    const close = s.startsWith("[") ? labelEnd(s, 1) : depth > 0 ? labelEnd(s, 0) : -1;
    if (close !== -1) {
      const label = s.slice(s.startsWith("[") ? 1 : 0, close);
      if (s.startsWith("[") && label.length <= LABEL_CAP) labels.add(normalised(label));
      let dest = destinationOf(s.slice(close + 2));
      const nextLine = lines[n + 1];
      const onNextLine = dest === "" && nextLine !== undefined;
      if (onNextLine) dest = destinationOf(nextLine.slice(prefixLength(nextLine)));
      if (!(dest === "" && isTokenLabel(label)) && !isAllowedDestination(dest)) {
        removed += 1;
        lines[n] = "";
        if (onNextLine && dest !== "") lines[n + 1] = "";
        depth = 0;
        continue;
      }
    }
    depth = Math.max(0, depth + bracketBalance(s));
  }
  return { text: lines.join("\n"), removed, labels };
}

function refClose(text: string, from: number): number {
  const stop = Math.min(text.length, from + SCAN_CAP);
  for (let j = from; j < stop; j += 1) {
    if (text[j] === "\\") j += 1;
    else if (text[j] === "[") return -1;
    else if (text[j] === "]") return j;
  }
  return -1;
}

function tagEnd(text: string, at: number): number {
  const found = matches(TAG_NAME, text, at);
  if (found === null || !BLOCKED_TAGS.has((found[1] ?? "").toLowerCase())) return -1;
  const stop = Math.min(text.length, at + SCAN_CAP);
  for (let j = at + 1; j < stop; j += 1) if (text[j] === ">") return j + 1;
  return at + 1;
}

export function neutraliseLinks(input: string): { text: string; removed: number } {
  const dropped = dropDefinitions(input);
  const text = dropped.text;
  const out: string[] = [];
  const open: Array<{ out: number; at: number }> = [];
  let removed = dropped.removed;
  let i = 0;
  while (i < text.length) {
    const c = text[i] as string;
    if (c === "[") {
      open.push({ out: out.length, at: i });
      out.push(c);
      i += 1;
    } else if (c === "]") {
      const opener = open.pop();
      const from = opener?.out;
      const label = opener === undefined ? "" : text.slice(opener.at + 1, i);
      const inline = text[i + 1] === "(" ? readDestination(text, i + 2) : undefined;
      const image = from !== undefined && out[from - 1] === "!";
      const refAt = text[i + 1] === "[" ? refClose(text, i + 2) : -1;
      const ref = refAt === -1 ? undefined : text.slice(i + 2, refAt);
      const exempt = /^\[cite:[^\]]*\]$/.test(label) || (isTokenLabel(label) && (ref === undefined || ref === "" || isTokenLabel(ref)));
      if (inline !== undefined && (image || !isAllowedDestination(inline.dest))) {
        removed += 1;
        if (from !== undefined) {
          if (image) out.length = from - 1;
          else out[from] = "";
        }
        i = closeParen(text, inline.end);
      } else if (from !== undefined && image) {
        removed += 1;
        out.length = from - 1;
        i = refAt === -1 ? i + 1 : refAt + 1;
      } else if (from !== undefined && !exempt && (ref !== undefined || dropped.labels.has(normalised(label)))) {
        removed += 1;
        out[from] = "";
        i = refAt === -1 ? i + 1 : refAt + 1;
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
    } else if (c === "/" && (i === 0 || /[\s("'<[=]/.test(text[i - 1] as string)) && matches(PROTOCOL_RELATIVE, text, i) !== null) {
      removed += 1;
      out.push(LINK_REMOVED);
      i = Math.max(i + 2, runEnd(text, i));
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
      const unresolved =
        i === text.length - 1 ||
        (text[i + 1] === "(" && !text.slice(i + 2, i + 2 + SCAN_CAP).includes(")")) ||
        (text[i + 1] === "[" && !text.slice(i + 2, i + 2 + SCAN_CAP).includes("]"));
      if (unresolved) hold = Math.min(hold, from);
    }
  }
  hold = Math.min(hold, open[0] ?? hold, tag === -1 ? hold : tag);
  if (text.endsWith("!")) hold = Math.min(hold, text.length - 1);
  return hold > 0 && text[hold] === "[" && text[hold - 1] === "!" ? hold - 1 : hold;
}
