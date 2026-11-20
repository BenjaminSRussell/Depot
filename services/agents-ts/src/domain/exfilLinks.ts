const GRAM = 8;
const LONG_SEGMENT = 24;
const OPAQUE_SEGMENT = /^[A-Za-z0-9+_-]{16,}={0,2}$/;
const ABSOLUTE = /^(?:https?:)?\/\//i;
const ABSOLUTE_PARTS = /^(?:https?:)?\/\/[^/?#]*([^?#]*)(?:\?([^#]*))?(?:#(.*))?$/i;
const HOST_PREFIX = /^(?:https?:)?\/\/[^/?#]*/i;
const MARKDOWN_RE = /(!?)\[([^\]]*)\]\(\s*<?([^)\s>]+)>?[^)]*\)/g;
const AUTOLINK_RE = /<((?:https?:)?\/\/[^>\s]+|data:[^>\s]+)>/gi;
const BARE_RE = /https?:\/\/[^\s)\]>"'<]+|data:[a-z]+\/[a-z0-9.+-]+[;,][^\s)\]>"'<]*/gi;
export const LINK_REMOVED = "[link removed]";

function decoded(text: string): string {
  try {
    return decodeURIComponent(text);
  } catch {
    return text;
  }
}

const grams = (text: string): string[] => Array.from({ length: Math.max(0, text.length - GRAM + 1) }, (_, i) => text.slice(i, i + GRAM)).filter((g) => /[A-Za-z0-9]/.test(g));

function inputGramsOf(inputs: readonly string[]): Set<string> {
  const found = new Set<string>();
  for (const source of inputs) {
    const variants = [source, encodeURIComponent(source)];
    for (const offset of [0, 1, 2]) {
      const b64 = Buffer.from(source.slice(offset)).toString("base64");
      variants.push(b64, b64.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, ""));
    }
    for (const variant of variants) for (const g of grams(variant)) found.add(g);
  }
  return found;
}

function carriesData(url: string, isImage: boolean, inputGrams: ReadonlySet<string>): boolean {
  if (/^data:/i.test(url)) return true;
  if (!ABSOLUTE.test(url)) return false;
  if (isImage) return true;
  const [, path = "", query = "", fragment = ""] = ABSOLUTE_PARTS.exec(url) ?? [];
  if (query.length > 0 || fragment.length > 0) return true;
  if (path.split("/").some((s) => s.length > LONG_SEGMENT || OPAQUE_SEGMENT.test(s))) return true;
  const tail = url.replace(HOST_PREFIX, "");
  return [tail, decoded(tail)].some((t) => grams(t).some((g) => inputGrams.has(g)));
}

export function neutraliseLinks(text: string, inputs: readonly string[] = []): { text: string; removed: number; urls: string[] } {
  const inputGrams = inputGramsOf(inputs);
  const urls: string[] = [];
  const strip = (url: string, isImage: boolean): boolean => {
    const carries = carriesData(url, isImage, inputGrams);
    if (carries) urls.push(url);
    return carries;
  };
  const cleaned = text
    .replace(MARKDOWN_RE, (whole, bang: string, label: string, url: string) => (strip(url, bang === "!") ? (bang === "!" ? "" : label) : whole))
    .replace(AUTOLINK_RE, (whole, url: string) => (strip(url, false) ? LINK_REMOVED : whole))
    .replace(BARE_RE, (url) => (strip(url, false) ? LINK_REMOVED : url));
  return { text: cleaned, removed: urls.length, urls };
}

export const dataCarryingLinks = (text: string, inputs: readonly string[] = []): string[] => neutraliseLinks(text, inputs).urls;
